//go:build loadtest

package store

// Load test of the metric storage at its full size: N series (default 5000 ≈ 100 devices)
// with the steady-state volume of the default retention – raw samples for 2 days (one per
// minute), 5-minute rollups for 35 days, hourly rollups for 400 days – then the timings of the
// operations the server runs: writing a minute of samples, the rollup, the hourly prune and
// chart queries. Run with:
//
//	LOADTEST_SERIES=5000 go test -tags loadtest -run TestLoad -v -timeout 60m ./internal/store/

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestLoad(t *testing.T) {
	n := 5000
	if v, err := strconv.Atoi(os.Getenv("LOADTEST_SERIES")); err == nil && v > 0 {
		n = v
	}
	const perDevice = 50
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := floorTo(time.Now().Unix(), Res1h) // an hour boundary keeps the numbers simple
	rawN, fiveN, hourN := DefaultRawDays*1440, DefaultFiveDays*288, DefaultHourDays*24

	// ---- generate ----
	start := time.Now()
	if _, err := s.db.Exec(`WITH RECURSIVE i(x) AS (SELECT 0 UNION ALL SELECT x + 1 FROM i WHERE x < ?1 - 1)
		INSERT INTO series (device_id, name, instance, unit, check_key, last_ts, last_value)
		SELECT 'dev' || (x / ?2), 'm' || (x % ?2), '', '%', 'check' || (x % ?2), ?3, 50 FROM i`, n, perDevice, now); err != nil {
		t.Fatal(err)
	}
	gen := func(label, q string, count int, args ...any) {
		t0 := time.Now()
		const chunk = 250 // series per transaction (keeps the WAL small)
		for lo := 1; lo <= n; lo += chunk {
			a := append([]any{lo, lo + chunk - 1, count}, args...)
			if _, err := s.db.Exec(q, a...); err != nil {
				t.Fatalf("%s: %v", label, err)
			}
		}
		t.Logf("generate %-8s %11d rows in %v", label, n*count, time.Since(t0).Round(time.Second))
	}
	// series outer, time inner: rows arrive in primary-key order per series
	gen("raw", `WITH RECURSIVE k(x) AS (SELECT 0 UNION ALL SELECT x + 1 FROM k WHERE x < ?3 - 1)
		INSERT INTO samples (series_id, ts, value)
		SELECT r.id, ?4 - (?3 - k.x) * 60, (r.id * 7 + k.x) % 100 FROM series r CROSS JOIN k WHERE r.id BETWEEN ?1 AND ?2`, rawN, now)
	gen("5min", `WITH RECURSIVE k(x) AS (SELECT 0 UNION ALL SELECT x + 1 FROM k WHERE x < ?3 - 1)
		INSERT INTO rollups (series_id, res, ts, min, max, avg, cnt)
		SELECT r.id, 300, ?4 - (?3 - k.x) * 300, (r.id + k.x) % 90, (r.id + k.x) % 90 + 9.5, (r.id + k.x) % 90 + 4.25, 5
		FROM series r CROSS JOIN k WHERE r.id BETWEEN ?1 AND ?2`, fiveN, now)
	gen("hourly", `WITH RECURSIVE k(x) AS (SELECT 0 UNION ALL SELECT x + 1 FROM k WHERE x < ?3 - 1)
		INSERT INTO rollups (series_id, res, ts, min, max, avg, cnt)
		SELECT r.id, 3600, ?4 - (?3 - k.x) * 3600, (r.id + k.x) % 90, (r.id + k.x) % 90 + 9.5, (r.id + k.x) % 90 + 4.25, 60
		FROM series r CROSS JOIN k WHERE r.id BETWEEN ?1 AND ?2`, hourN, now)
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated %d series in %v; database %.2f GB", n, time.Since(start).Round(time.Second), float64(s.DBSize())/1e9)

	// the state a server has after running for a year
	s.rmu.Lock()
	s.roll = rollState{Five: now, Hour: now, RawCut: now - int64(rawN)*60, FiveCut: now - int64(fiveN)*300}
	s.done = s.roll
	s.rmu.Unlock()

	timed := func(label string, f func() error) time.Duration {
		t.Helper()
		t0 := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		d := time.Since(t0)
		t.Logf("%-46s %10v", label, d.Round(100*time.Microsecond))
		return d
	}

	// ---- one minute of writes: every device reports its 50 metrics ----
	devices := n / perDevice
	writeMinute := func(ts int64) error {
		for d := range devices {
			ms := make([]model.Metric, perDevice)
			for m := range ms {
				ms[m] = model.Metric{Name: fmt.Sprintf("m%d", m), Value: float64((d + m + int(ts)) % 100), Unit: "%"}
			}
			if err := s.WriteMetrics(fmt.Sprintf("dev%d", d), ts, []model.Check{{Key: "check", Metrics: ms}}); err != nil {
				return err
			}
		}
		return nil
	}
	timed(fmt.Sprintf("write 1 minute, first (%d devices)", devices), func() error { return writeMinute(now + 60) })
	for i := 2; i <= 5; i++ {
		timed(fmt.Sprintf("write 1 minute (%d devices × %d values)", devices, perDevice), func() error { return writeMinute(now + int64(i)*60) })
	}
	timed("rollup (one 5-minute bucket)", func() error { return s.Rollup(now + 6*60 + rollGrace) })
	for i := 6; i <= 60; i++ {
		if err := writeMinute(now + int64(i)*60); err != nil {
			t.Fatal(err)
		}
	}
	timed("rollup (an hour boundary)", func() error { return s.Rollup(now + 3600 + rollGrace + 60) })
	timed("prune (hourly run)", func() error { return s.PruneMetrics(now+3600+120, 0, 0, 0) })

	// ---- chart queries ----
	ids := func(k int) []int64 {
		out := make([]int64, k)
		for i := range out {
			out[i] = int64(i + 1)
		}
		return out
	}
	to := now + 3600
	for _, q := range []struct {
		label  string
		span   int64
		series int
	}{
		{"chart 1 h, 1 series", 3600, 1},
		{"chart 24 h, 1 series", 86400, 1},
		{"chart 7 days, 1 series", 7 * 86400, 1},
		{"chart 30 days, 1 series", 30 * 86400, 1},
		{"chart 1 year, 1 series", 365 * 86400, 1},
		{"chart 24 h, 16 series", 86400, 16},
		{"chart 30 days, 16 series", 30 * 86400, 16},
		{"chart 1 year, 16 series", 365 * 86400, 16},
		{"sparklines 6 h, 50 series (one device)", 6 * 3600, 50},
	} {
		step := max(q.span/300, 60)
		var pts int
		timed(q.label, func() error {
			r, err := s.Query(ids(q.series), to-q.span, to, step)
			for _, p := range r {
				pts += len(p)
			}
			return err
		})
		if pts == 0 {
			t.Errorf("%s: no points", q.label)
		}
	}
	t.Logf("database after the run %.2f GB", float64(s.DBSize())/1e9)
}
