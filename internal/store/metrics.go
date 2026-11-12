package store

import (
	"database/sql"
	"math"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Default retention in days (used when PruneMetrics gets a value <= 0).
const (
	DefaultRawDays  = 2
	DefaultFiveDays = 35
	DefaultHourDays = 400
)

// Series is one time series: a metric (name + instance) of a device.
type Series struct {
	ID        int64   `json:"id"`
	DeviceID  string  `json:"device_id"`
	Name      string  `json:"name"`
	Instance  string  `json:"instance"`
	Unit      string  `json:"unit"`
	CheckKey  string  `json:"check_key"`
	LastTS    int64   `json:"last_ts"`
	LastValue float64 `json:"last_value"`
}

func seriesCacheKey(deviceID, name, instance string) string {
	return deviceID + "\x00" + name + "\x00" + instance
}

// WriteMetrics stores all metrics of the checks as samples at time ts (unix seconds, 0 = now).
// Series are created on first use; unit, check key and the latest value are kept current
// (older samples, e.g. from buffered agent reports, never replace a newer latest value).
// Non-finite values are skipped.
func (s *Store) WriteMetrics(deviceID string, ts int64, checks []model.Check) error {
	if ts == 0 {
		ts = time.Now().Unix()
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	update, err := tx.Prepare(`UPDATE series SET unit = ?1, check_key = ?2,
		last_value = CASE WHEN ?3 >= last_ts THEN ?4 ELSE last_value END,
		last_ts = MAX(last_ts, ?3)
		WHERE id = ?5`)
	if err != nil {
		return err
	}
	upsert, err := tx.Prepare(`INSERT INTO series (device_id, name, instance, unit, check_key, last_ts, last_value)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (device_id, name, instance) DO UPDATE SET unit = excluded.unit, check_key = excluded.check_key,
			last_value = CASE WHEN excluded.last_ts >= series.last_ts THEN excluded.last_value ELSE series.last_value END,
			last_ts = MAX(series.last_ts, excluded.last_ts)
		RETURNING id`)
	if err != nil {
		return err
	}
	insert, err := tx.Prepare("INSERT OR REPLACE INTO samples (series_id, ts, value) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}

	fresh := map[string]int64{} // IDs learned in this transaction, cached after commit
	written := 0
	for _, c := range checks {
		for _, m := range c.Metrics {
			if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
				continue
			}
			key := seriesCacheKey(deviceID, m.Name, m.Instance)
			id, ok := fresh[key]
			if !ok {
				id, ok = s.series[key]
			}
			if ok {
				res, err := update.Exec(m.Unit, c.Key, ts, m.Value, id)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 0 {
					ok = false // series was pruned meanwhile
				}
			}
			if !ok {
				if err := upsert.QueryRow(deviceID, m.Name, m.Instance, m.Unit, c.Key, ts, m.Value).Scan(&id); err != nil {
					return err
				}
				fresh[key] = id
			}
			if _, err := insert.Exec(id, ts, m.Value); err != nil {
				return err
			}
			written++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for k, id := range fresh {
		s.series[k] = id
	}
	if written > 0 {
		return s.markDirty(ts)
	}
	return nil
}

const seriesCols = "id, device_id, name, instance, unit, check_key, last_ts, last_value"

func scanSeries(rows *sql.Rows) ([]Series, error) {
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		var x Series
		if err := rows.Scan(&x.ID, &x.DeviceID, &x.Name, &x.Instance, &x.Unit, &x.CheckKey, &x.LastTS, &x.LastValue); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SeriesList returns all series of a device, ordered by name and instance.
func (s *Store) SeriesList(deviceID string) ([]Series, error) {
	rows, err := s.db.Query("SELECT "+seriesCols+" FROM series WHERE device_id = ? ORDER BY name, instance", deviceID)
	if err != nil {
		return nil, err
	}
	return scanSeries(rows)
}

// LatestSeries returns the series with the given names of all devices (all series when no
// name is given), e.g. cpu.usage, mem.usage and disk.usage for the overview.
func (s *Store) LatestSeries(names ...string) ([]Series, error) {
	q := "SELECT " + seriesCols + " FROM series"
	args := make([]any, len(names))
	if len(names) > 0 {
		q += " WHERE name IN (?" + strings.Repeat(", ?", len(names)-1) + ")"
		for i, n := range names {
			args[i] = n
		}
	}
	rows, err := s.db.Query(q+" ORDER BY device_id, name, instance", args...)
	if err != nil {
		return nil, err
	}
	return scanSeries(rows)
}

// SeriesCount returns the number of series.
func (s *Store) SeriesCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM series").Scan(&n)
	return n, err
}

// DBSize returns the size of the main database file in bytes (without the WAL).
func (s *Store) DBSize() int64 {
	var n int64
	if err := s.db.QueryRow("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&n); err != nil {
		return 0
	}
	return n
}

// DeleteDeviceData removes everything the store keeps for a device: series, samples,
// rollups, the persisted status and the agent token. Alerts stay as history.
func (s *Store) DeleteDeviceData(deviceID string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DELETE FROM samples WHERE series_id IN (SELECT id FROM series WHERE device_id = ?)",
		"DELETE FROM rollups WHERE series_id IN (SELECT id FROM series WHERE device_id = ?)",
		"DELETE FROM series WHERE device_id = ?",
		"DELETE FROM statuses WHERE device_id = ?",
		"DELETE FROM agent_tokens WHERE device_id = ?",
	} {
		if _, err := tx.Exec(q, deviceID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	prefix := deviceID + "\x00"
	for k := range s.series {
		if strings.HasPrefix(k, prefix) {
			delete(s.series, k)
		}
	}
	return nil
}

// PruneMetrics deletes raw samples, 5-minute and hourly rollups older than the given number of
// days (<= 0 = default) and series that have had no value for longer than all of them.
// Cut-offs are aligned to full hours so no rollup bucket is cut in half. Data that is not rolled
// up yet (Rollup not run, or late samples pending) is kept until the next Rollup.
func (s *Store) PruneMetrics(now int64, rawDays, fiveDays, hourDays int) error {
	cut := func(days, def int) int64 {
		if days <= 0 {
			days = def
		}
		return floorTo(now-int64(days)*86400, Res1h)
	}
	rawCut := cut(rawDays, DefaultRawDays)
	fiveCut := cut(fiveDays, DefaultFiveDays)
	hourCut := cut(hourDays, DefaultHourDays)
	seriesCut := min(rawCut, fiveCut, hourCut)

	// Lock order: rollMu, wmu, rmu. With rollMu and wmu held, no Rollup runs and no sample is
	// written, so the rollup state cannot change until the deletes are committed.
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.rmu.Lock()
	rs := s.roll
	s.rmu.Unlock()
	rawCut = min(rawCut, floorTo(rs.Five, Res1h))
	fiveCut = min(fiveCut, rs.Hour)
	if rs.Dirty != 0 {
		rawCut = min(rawCut, floorTo(rs.Dirty, Res1h))
		fiveCut = min(fiveCut, floorTo(rs.Dirty, Res1h))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// "series_id IN (SELECT id FROM series)" lets SQLite seek the primary key per series
	// instead of scanning the whole table.
	steps := []struct {
		q    string
		args []any
	}{
		{"DELETE FROM samples WHERE series_id IN (SELECT id FROM series) AND ts < ?", []any{rawCut}},
		{"DELETE FROM rollups WHERE series_id IN (SELECT id FROM series) AND res = 300 AND ts < ?", []any{fiveCut}},
		{"DELETE FROM rollups WHERE series_id IN (SELECT id FROM series) AND res = 3600 AND ts < ?", []any{hourCut}},
		{"DELETE FROM samples WHERE series_id IN (SELECT id FROM series WHERE last_ts < ?)", []any{seriesCut}},
		{"DELETE FROM rollups WHERE series_id IN (SELECT id FROM series WHERE last_ts < ?)", []any{seriesCut}},
		{"DELETE FROM series WHERE last_ts < ?", []any{seriesCut}},
	}
	var removed int64
	for i, st := range steps {
		res, err := tx.Exec(st.q, st.args...)
		if err != nil {
			return err
		}
		if i == len(steps)-1 {
			removed, _ = res.RowsAffected()
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if removed > 0 {
		clear(s.series)
	}

	s.rmu.Lock()
	defer s.rmu.Unlock()
	if rawCut <= s.roll.RawCut && fiveCut <= s.roll.FiveCut {
		return nil
	}
	s.roll.RawCut = max(s.roll.RawCut, rawCut)
	s.roll.FiveCut = max(s.roll.FiveCut, fiveCut)
	s.done.RawCut, s.done.FiveCut = s.roll.RawCut, s.roll.FiveCut
	return s.saveRoll()
}
