package store

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"pk95monitoring/internal/model"
)

const base = int64(1_700_006_400) // midnight UTC, multiple of 86400

func chk(key string, ms ...model.Metric) model.Check {
	return model.Check{Key: key, Name: key, State: model.StateOK, Metrics: ms}
}

func met(name, inst string, v float64, unit string) model.Metric {
	return model.Metric{Name: name, Instance: inst, Value: v, Unit: unit}
}

// writeOne writes a single metric and returns its series id.
func writeOne(t testing.TB, s *Store, dev, name string, ts int64, v float64) int64 {
	t.Helper()
	if err := s.WriteMetrics(dev, ts, []model.Check{chk("cpu", met(name, "", v, "%"))}); err != nil {
		t.Fatal(err)
	}
	list, err := s.SeriesList(dev)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Name == name {
			return x.ID
		}
	}
	t.Fatalf("series %s/%s not found", dev, name)
	return 0
}

// bulk inserts raw samples directly (fast setup for large ranges).
func bulk(t testing.TB, s *Store, id int64, from, to, every int64, val func(ts int64) float64) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.Prepare("INSERT OR REPLACE INTO samples (series_id, ts, value) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	for ts := from; ts < to; ts += every {
		if _, err := st.Exec(id, ts, val(ts)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec("UPDATE series SET last_ts = MAX(last_ts, ?) WHERE id = ?", to-every, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteMetricsAndSeriesList(t *testing.T) {
	s := newStore(t)
	checks := []model.Check{
		chk("cpu", met("cpu.usage", "", 12.5, "%"), met("cpu.load1", "", 0.7, "")),
		chk("disk:/", met("disk.usage", "/", 40, "%"), met("disk.used", "/", 4e9, "B")),
		chk("disk:/var", met("disk.usage", "/var", 91, "%"), met("disk.used", "/var", math.NaN(), "B")),
		{Key: "system", State: model.StateOK}, // no metrics
	}
	if err := s.WriteMetrics("d1", base, checks); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetrics("d2", base, checks[:1]); err != nil {
		t.Fatal(err)
	}
	list, err := s.SeriesList("d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 { // NaN skipped
		t.Fatalf("got %d series: %+v", len(list), list)
	}
	want := Series{DeviceID: "d1", Name: "disk.usage", Instance: "/var", Unit: "%", CheckKey: "disk:/var", LastTS: base, LastValue: 91}
	got := list[3] // ordered by name, instance: cpu.load1, cpu.usage, disk.usage /, disk.usage /var, disk.used /
	want.ID = got.ID
	if got != want {
		t.Fatalf("series = %+v, want %+v", got, want)
	}

	// A newer value updates last_*, an older one (buffered report) only adds a sample.
	if err := s.WriteMetrics("d1", base+60, []model.Check{chk("disk:/var", met("disk.usage", "/var", 93, "%"))}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetrics("d1", base-60, []model.Check{chk("disk:/var", met("disk.usage", "/var", 80, "%"))}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.SeriesList("d1")
	if x := list[3]; x.LastTS != base+60 || x.LastValue != 93 || x.ID != want.ID {
		t.Fatalf("after updates: %+v", x)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", want.ID); n != 3 {
		t.Fatalf("samples = %d, want 3", n)
	}

	latest, err := s.LatestSeries("cpu.usage", "disk.usage")
	if err != nil || len(latest) != 4 {
		t.Fatalf("LatestSeries = %d %v", len(latest), err)
	}
	if all, _ := s.LatestSeries(); len(all) != 7 {
		t.Fatalf("LatestSeries() = %d, want 7", len(all))
	}
	if n, err := s.SeriesCount(); err != nil || n != 7 {
		t.Fatalf("SeriesCount = %d %v", n, err)
	}
	b, _ := json.Marshal(list[0])
	if string(b) != fmt.Sprintf(`{"id":%d,"device_id":"d1","name":"cpu.load1","instance":"","unit":"","check_key":"cpu","last_ts":%d,"last_value":0.7}`, list[0].ID, base) {
		t.Fatalf("json = %s", b)
	}
}

func TestPointJSON(t *testing.T) {
	tests := []struct {
		p    Point
		want string
	}{
		{Point{100, 1.5, 1, 2}, `[100,1.5,1,2]`},
		{Point{5, 34359738368, 0, 1e-9}, `[5,34359738368,0,1e-09]`},
		{Point{7, math.NaN(), 0, 0}, `[7,null,0,0]`},
	}
	for _, tt := range tests {
		b, err := json.Marshal(tt.p)
		if err != nil || string(b) != tt.want {
			t.Errorf("Marshal(%v) = %s %v, want %s", tt.p, b, err, tt.want)
		}
	}
	var p Point
	if err := json.Unmarshal([]byte(`[100,1.5,1,2]`), &p); err != nil || p != (Point{100, 1.5, 1, 2}) {
		t.Fatalf("Unmarshal = %+v %v", p, err)
	}
}

func TestQueryRaw(t *testing.T) {
	s := newStore(t)
	id := writeOne(t, s, "d1", "cpu.usage", base, 0)
	other := writeOne(t, s, "d1", "mem.usage", base, 50)
	for i := int64(1); i < 60; i++ {
		if i >= 20 && i < 30 {
			continue // gap: bucket 2 stays empty
		}
		writeOne(t, s, "d1", "cpu.usage", base+60*i, float64(i))
	}
	res, err := s.Query([]int64{id, other, 9999, id}, base, base+3599, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || len(res[9999]) != 0 || res[9999] == nil {
		t.Fatalf("result keys: %v", res)
	}
	want := []Point{{base, 4.5, 0, 9}, {base + 600, 14.5, 10, 19}, {base + 1800, 34.5, 30, 39},
		{base + 2400, 44.5, 40, 49}, {base + 3000, 54.5, 50, 59}}
	if !reflect.DeepEqual(res[id], want) {
		t.Fatalf("points = %v\nwant     %v", res[id], want)
	}
	if !reflect.DeepEqual(res[other], []Point{{base, 50, 50, 50}}) {
		t.Fatalf("other = %v", res[other])
	}
	// step 0: every sample its own bucket
	res, _ = s.Query([]int64{id}, base, base+299, 0)
	if len(res[id]) != 5 || res[id][4] != (Point{base + 240, 4, 4, 4}) {
		t.Fatalf("step 0 = %v", res[id])
	}
	if res, _ := s.Query([]int64{id}, base+10, base, 60); len(res[id]) != 0 {
		t.Fatal("to < from must be empty")
	}
}

type rollRow struct {
	ID, Res, TS        int64
	Min, Max, Avg, Cnt float64
}

func rollups(t *testing.T, s *Store) []rollRow {
	t.Helper()
	rows, err := s.db.Query("SELECT series_id, res, ts, min, max, avg, cnt FROM rollups ORDER BY series_id, res, ts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []rollRow
	for rows.Next() {
		var r rollRow
		if err := rows.Scan(&r.ID, &r.Res, &r.TS, &r.Min, &r.Max, &r.Avg, &r.Cnt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func findRoll(rs []rollRow, res, ts int64) rollRow {
	for _, r := range rs {
		if r.Res == res && r.TS == ts {
			return r
		}
	}
	return rollRow{}
}

func TestRollup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(base); err != nil { // empty database
		t.Fatal(err)
	}
	var id int64
	for i := int64(0); i < 120; i++ { // two hours, one value per minute: 0 … 119
		id = writeOne(t, s, "d1", "cpu.usage", base+60*i, float64(i))
	}
	now := base + 7200 + rollGrace
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}
	rs := rollups(t, s)
	if len(rs) != 24+2 {
		t.Fatalf("got %d rollups, want 26", len(rs))
	}
	for k := int64(0); k < 24; k++ {
		r := findRoll(rs, Res5m, base+300*k)
		want := rollRow{id, Res5m, base + 300*k, float64(5 * k), float64(5*k + 4), float64(5*k + 2), 5}
		if r != want {
			t.Fatalf("5m bucket %d = %+v, want %+v", k, r, want)
		}
	}
	if r := findRoll(rs, Res1h, base); r != (rollRow{id, Res1h, base, 0, 59, 29.5, 60}) {
		t.Fatalf("hour 0 = %+v", r)
	}
	if r := findRoll(rs, Res1h, base+3600); r != (rollRow{id, Res1h, base + 3600, 60, 119, 89.5, 60}) {
		t.Fatalf("hour 1 = %+v", r)
	}

	// Idempotent, also after reopening (state persisted).
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Rollup(now + 30); err != nil {
		t.Fatal(err)
	}
	if again := rollups(t, s); !reflect.DeepEqual(again, rs) {
		t.Fatalf("rollup not idempotent:\n%v\n%v", again, rs)
	}

	// A late sample in an already rolled-up bucket is picked up next time.
	writeOne(t, s, "d1", "cpu.usage", base+30, 100)
	if err := s.Rollup(now + 60); err != nil {
		t.Fatal(err)
	}
	rs = rollups(t, s)
	if r := findRoll(rs, Res5m, base); r.Cnt != 6 || r.Max != 100 || r.Min != 0 || math.Abs(r.Avg-110.0/6) > 1e-9 {
		t.Fatalf("late 5m bucket = %+v", r)
	}
	if r := findRoll(rs, Res1h, base); r.Cnt != 61 || r.Max != 100 || math.Abs(r.Avg-(29.5*60+100)/61) > 1e-9 {
		t.Fatalf("late hour = %+v", r)
	}

	// Incremental: the third hour is added once complete (not before).
	for i := int64(120); i < 180; i++ {
		writeOne(t, s, "d1", "cpu.usage", base+60*i, 1)
	}
	if err := s.Rollup(base + 3*3600 - 1); err != nil {
		t.Fatal(err)
	}
	if r := findRoll(rollups(t, s), Res1h, base+7200); r.Cnt != 0 {
		t.Fatalf("incomplete hour rolled up: %+v", r)
	}
	if err := s.Rollup(base + 3*3600 + rollGrace); err != nil {
		t.Fatal(err)
	}
	if r := findRoll(rollups(t, s), Res1h, base+7200); r != (rollRow{id, Res1h, base + 7200, 1, 1, 1, 60}) {
		t.Fatalf("hour 2 = %+v", r)
	}
}

func TestQueryRollups(t *testing.T) {
	s := newStore(t)
	id := writeOne(t, s, "d1", "cpu.usage", base, 0)
	week := int64(7 * 86400)
	hourIdx := func(ts int64) float64 { return float64((ts - base) / 3600) }
	bulk(t, s, id, base, base+week, 300, hourIdx)
	if err := s.Rollup(base + week + rollGrace); err != nil {
		t.Fatal(err)
	}
	// Not rolled up yet: answered from raw samples.
	writeOne(t, s, "d1", "cpu.usage", base+week+10, 999)

	// 7 days → 5-minute rollups.
	res, err := s.Query([]int64{id}, base, base+week+59, 3600)
	if err != nil {
		t.Fatal(err)
	}
	pts := res[id]
	if len(pts) != 7*24+1 {
		t.Fatalf("got %d points, want %d", len(pts), 7*24+1)
	}
	for h, p := range pts[:7*24] {
		v := float64(h)
		if p != (Point{base + int64(h)*3600, v, v, v}) {
			t.Fatalf("hour %d = %v", h, p)
		}
	}
	if last := pts[len(pts)-1]; last != (Point{base + week, 999, 999, 999}) {
		t.Fatalf("raw tail = %v", last)
	}

	// Remove raw data: the 7-day answer must not depend on it.
	if _, err := s.db.Exec("DELETE FROM samples WHERE ts < ?", base+week); err != nil {
		t.Fatal(err)
	}
	res, _ = s.Query([]int64{id}, base, base+week+59, 3600)
	if len(res[id]) != 7*24+1 {
		t.Fatalf("without raw: %d points", len(res[id]))
	}

	// One year → hourly rollups, one bucket per day.
	res, err = s.Query([]int64{id}, base+week-365*86400, base+week+59, 86400)
	if err != nil {
		t.Fatal(err)
	}
	pts = res[id]
	if len(pts) != 8 {
		t.Fatalf("daily points = %v", pts)
	}
	for d, p := range pts[:7] {
		lo := float64(24 * d)
		if p != (Point{base + int64(d)*86400, lo + 11.5, lo, lo + 23}) {
			t.Fatalf("day %d = %v", d, p)
		}
	}
	if pts[7] != (Point{base + week, 999, 999, 999}) {
		t.Fatalf("daily tail = %v", pts[7])
	}
	// Step below the source resolution is raised to it.
	res, _ = s.Query([]int64{id}, base, base+3*86400, 60)
	if p := res[id]; len(p) < 2 || p[1].TS-p[0].TS != Res5m {
		t.Fatalf("step not raised: %v", p)
	}
}

func TestSourceRes(t *testing.T) {
	tests := []struct{ span, want int64 }{
		{3600, 0}, {48 * 3600, 0}, {48*3600 + 1, Res5m}, {35 * 86400, Res5m}, {35*86400 + 1, Res1h}, {365 * 86400, Res1h},
	}
	for _, tt := range tests {
		if got := sourceRes(tt.span); got != tt.want {
			t.Errorf("sourceRes(%d) = %d, want %d", tt.span, got, tt.want)
		}
	}
}

func TestPruneMetrics(t *testing.T) {
	s := newStore(t)
	now := base + 1000*86400
	id := writeOne(t, s, "d1", "cpu.usage", now-3*86400, 1)
	writeOne(t, s, "d1", "cpu.usage", now-3600, 2)
	old := writeOne(t, s, "d2", "cpu.usage", now-500*86400, 3)
	ins := func(sid, res, ts int64) {
		if _, err := s.db.Exec("INSERT INTO rollups (series_id, res, ts, min, max, avg, cnt) VALUES (?, ?, ?, 1, 1, 1, 1)", sid, res, ts); err != nil {
			t.Fatal(err)
		}
	}
	ins(id, Res5m, now-40*86400)
	ins(id, Res5m, now-10*86400)
	ins(id, Res1h, now-500*86400)
	ins(id, Res1h, now-100*86400)
	ins(old, Res1h, now-500*86400)

	rolledUpTo(s, now)
	if err := s.PruneMetrics(now, 2, 35, 0); err != nil { // 0 = default 400 days
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", id); n != 1 {
		t.Fatalf("raw samples left = %d, want 1", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM rollups WHERE series_id = ? AND res = 300", id); n != 1 {
		t.Fatalf("5m rollups left = %d", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM rollups WHERE series_id = ? AND res = 3600", id); n != 1 {
		t.Fatalf("1h rollups left = %d", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM series WHERE id = ?", old); n != 0 {
		t.Fatal("stale series not removed")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", old) +
		count(t, s, "SELECT COUNT(*) FROM rollups WHERE series_id = ?", old); n != 0 {
		t.Fatal("data of stale series not removed")
	}
	// The cached ID of the pruned series must not be reused.
	if again := writeOne(t, s, "d2", "cpu.usage", now, 4); again == old {
		t.Fatal("pruned series id reused")
	}
}

// rolledUpTo sets the rollup state as if Rollup(now) had run (without the work).
func rolledUpTo(s *Store, now int64) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.roll.Five, s.roll.Hour = floorTo(now, Res5m), floorTo(now, Res1h)
	s.done = s.roll
}

func TestPruneKeepsUnrolledData(t *testing.T) {
	s := newStore(t)
	now := base + 10*86400
	id := writeOne(t, s, "d1", "cpu.usage", now-5*86400, 1)
	writeOne(t, s, "d1", "cpu.usage", now-5*86400+60, 3)
	writeOne(t, s, "d1", "cpu.usage", now-60, 2)

	// Rollup never ran: nothing raw may be deleted, the samples are the only copy.
	if err := s.PruneMetrics(now, 2, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", id); n != 3 {
		t.Fatalf("samples after prune without rollup = %d, want 3", n)
	}
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}
	// A late sample below the rollup watermark keeps its hour until the next Rollup.
	writeOne(t, s, "d1", "cpu.usage", now-4*86400, 5)
	if err := s.PruneMetrics(now, 2, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ? AND ts = ?", id, now-4*86400); n != 1 {
		t.Fatal("dirty sample pruned before it was rolled up")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ? AND ts < ?", id, now-4*86400); n != 0 {
		t.Fatal("rolled-up samples older than the dirty hour must be pruned")
	}
	if err := s.Rollup(now + 60); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneMetrics(now+60, 2, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", id); n != 1 {
		t.Fatalf("samples after rollup and prune = %d, want 1", n)
	}
	rs := rollups(t, s)
	if r := findRoll(rs, Res5m, now-5*86400); r.Cnt != 2 || r.Avg != 2 {
		t.Fatalf("5m rollup of pruned samples = %+v", r)
	}
	if r := findRoll(rs, Res5m, now-4*86400); r.Cnt != 1 || r.Avg != 5 {
		t.Fatalf("5m rollup of late sample = %+v", r)
	}
}

func TestQueryShortRetention(t *testing.T) {
	s := newStore(t)
	id := writeOne(t, s, "d1", "cpu.usage", base, 0)
	days := int64(40 * 86400)
	bulk(t, s, id, base, base+days, 300, func(ts int64) float64 { return float64((ts - base) / 3600) })
	now := base + days
	if err := s.Rollup(now + rollGrace); err != nil {
		t.Fatal(err)
	}
	// Raw data kept 1 day, 5-minute rollups 7 days.
	if err := s.PruneMetrics(now, 1, 7, 0); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		from, to, step int64
	}{
		{"48h from raw + 5m", now - 48*3600, now - 1, 3600},
		{"30d from 5m + 1h", now - 30*86400, now - 1, 3600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.Query([]int64{id}, tt.from, tt.to, tt.step)
			if err != nil {
				t.Fatal(err)
			}
			pts := res[id]
			if want := int((tt.to + 1 - tt.from) / tt.step); len(pts) != want {
				t.Fatalf("got %d points, want %d (gap where data is pruned)", len(pts), want)
			}
			for i, p := range pts {
				v := float64((p.TS - base) / 3600)
				if p.TS != tt.from+int64(i)*tt.step || p.Avg != v || p.Min != v || p.Max != v {
					t.Fatalf("point %d = %v, want value %v", i, p, v)
				}
			}
		})
	}
	// The cut-offs survive a restart.
	var st rollState
	if err := s.GetSetting(rollKey, &st); err != nil || st.RawCut != now-86400 || st.FiveCut != now-7*86400 {
		t.Fatalf("persisted state = %+v %v", st, err)
	}
}

func TestRollupInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for i := int64(0); i < 120; i++ {
		id = writeOne(t, s, "d1", "cpu.usage", base+60*i, 7)
	}
	if err := s.Rollup(base + 3600 + rollGrace); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash during the next Rollup: the second hour is claimed, a late sample
	// persists the state, and the process dies before the work is done.
	s.rmu.Lock()
	s.roll.Five, s.inflight = base+7200, base+3600
	s.rmu.Unlock()
	writeOne(t, s, "d1", "cpu.usage", base+7000, 7)
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.roll.Dirty != base+3600 || s.done.Five != base+3600 || s.done.Hour != base+3600 {
		t.Fatalf("state after reopen: roll %+v done %+v", s.roll, s.done)
	}
	// Until the rollup is redone, Query reads the claimed range from raw samples.
	res, _ := s.Query([]int64{id}, base+7200-3*86400, base+7199, 3600)
	if p := res[id]; len(p) != 2 || p[1] != (Point{base + 3600, 7, 7, 7}) {
		t.Fatalf("query before redo = %v", p)
	}
	if err := s.Rollup(base + 7200 + rollGrace); err != nil {
		t.Fatal(err)
	}
	rs := rollups(t, s)
	if r := findRoll(rs, Res5m, base+3600); r.Cnt != 5 {
		t.Fatalf("claimed range not rolled up after restart: %+v", r)
	}
	if r := findRoll(rs, Res1h, base+3600); r.Cnt != 61 {
		t.Fatalf("claimed hour = %+v", r)
	}
}

func TestRollupClockBack(t *testing.T) {
	s := newStore(t)
	for i := int64(0); i < 60; i++ {
		writeOne(t, s, "d1", "cpu.usage", base+60*i, 1)
	}
	if err := s.Rollup(base + 3600 + rollGrace); err != nil {
		t.Fatal(err)
	}
	// A late sample in the last bucket, then a Rollup with a clock that went back 10 minutes.
	writeOne(t, s, "d1", "cpu.usage", base+3300+30, 13)
	if err := s.Rollup(base + 3000 + rollGrace); err != nil {
		t.Fatal(err)
	}
	rs := rollups(t, s)
	if r := findRoll(rs, Res5m, base+3300); r.Cnt != 6 || r.Max != 13 {
		t.Fatalf("late bucket = %+v", r)
	}
	if r := findRoll(rs, Res1h, base); r.Cnt != 61 || r.Max != 13 {
		t.Fatalf("hour = %+v", r)
	}
}

func TestDeleteDeviceData(t *testing.T) {
	s := newStore(t)
	id := writeOne(t, s, "d1", "cpu.usage", base, 1)
	keep := writeOne(t, s, "d2", "cpu.usage", base, 1)
	if err := s.Rollup(base + 600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveStatus(model.DeviceStatus{DeviceID: "d1", State: model.StateOK}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentToken("d1", "secret"); err != nil {
		t.Fatal(err)
	}
	aid, err := s.OpenAlert(model.Alert{DeviceID: "d1", CheckKey: "cpu", Severity: model.StateWarn})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDeviceData("d1"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.SeriesList("d1"); len(l) != 0 {
		t.Fatal("series left")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", id) +
		count(t, s, "SELECT COUNT(*) FROM rollups WHERE series_id = ?", id); n != 0 {
		t.Fatal("samples/rollups left")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples WHERE series_id = ?", keep); n != 1 {
		t.Fatal("other device touched")
	}
	if st, _ := s.LoadStatuses(); len(st) != 0 {
		t.Fatal("status left")
	}
	if _, err := s.AgentDevice("secret"); err == nil {
		t.Fatal("agent token left")
	}
	if a, _ := s.ActiveAlerts(); len(a) != 1 || a[0].ID != aid {
		t.Fatal("alerts must stay")
	}
	if again := writeOne(t, s, "d1", "cpu.usage", base+60, 2); again == id {
		t.Fatal("deleted series id reused (stale cache)")
	}
}

func TestConcurrentWriteMetrics(t *testing.T) {
	s := newStore(t)
	const workers, rounds, metrics = 8, 25, 40
	var wg sync.WaitGroup
	errs := make(chan error, workers+1)
	for g := range workers {
		wg.Go(func() {
			for i := range rounds {
				ts := base + int64(g*1000+i)
				var own, shared []model.Metric
				for m := range metrics {
					own = append(own, met("disk.usage", fmt.Sprintf("/m%d", m), float64(i), "%"))
					shared = append(shared, met("if.rx", fmt.Sprintf("p%d", m), float64(g), "B/s"))
				}
				if err := s.WriteMetrics(fmt.Sprintf("dev%d", g), ts, []model.Check{chk("disk", own...)}); err != nil {
					errs <- err
					return
				}
				if err := s.WriteMetrics("shared", ts, []model.Check{chk("if", shared...)}); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 5 {
			if err := s.Rollup(base + int64(i)*300); err != nil {
				errs <- err
				return
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n, _ := s.SeriesCount(); n != workers*metrics+metrics {
		t.Fatalf("series = %d, want %d", n, workers*metrics+metrics)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM samples"); n != 2*workers*rounds*metrics {
		t.Fatalf("samples = %d, want %d", n, 2*workers*rounds*metrics)
	}
}

func BenchmarkWriteMetrics1000(b *testing.B) {
	s := newStore(b)
	var checks []model.Check
	for c := range 100 {
		var ms []model.Metric
		for m := range 10 {
			ms = append(ms, met(fmt.Sprintf("m%d", m), fmt.Sprintf("i%d", c), float64(c*m), ""))
		}
		checks = append(checks, chk(fmt.Sprintf("c%d", c), ms...))
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := s.WriteMetrics("dev", base+int64(i)*60, checks); err != nil {
			b.Fatal(err)
		}
	}
}
