package store

import (
	"database/sql"
	"fmt"
)

// Rollup resolutions in seconds.
const (
	Res5m = 300
	Res1h = 3600
)

const (
	rollKey   = "store.rollup" // settings key of the persisted rollState
	rollGrace = 60             // seconds a 5-minute bucket stays open after its end for late samples
	// Work is split into chunks so writers never wait long for the database.
	rollChunk5 = 3 * 3600
	rollChunk1 = 2 * 86400
)

// rollState is the progress of the rollup.
type rollState struct {
	Five int64 `json:"five"` // 5-minute buckets before this time are rolled up (0 = never)
	Hour int64 `json:"hour"` // hourly buckets before this time are rolled up (0 = never)
	// Dirty is the earliest sample written below Five since the last rollup (0 = none),
	// e.g. from buffered agent reports. Its buckets are rolled up again.
	Dirty int64 `json:"dirty,omitempty"`
	// RawCut and FiveCut are the oldest times still kept as raw samples and 5-minute rollups
	// (set by PruneMetrics). Query reads older parts of a range from the coarser source.
	RawCut  int64 `json:"raw_cut,omitempty"`
	FiveCut int64 `json:"five_cut,omitempty"`
}

// pending returns the start of the range that is claimed or marked dirty but not rolled up
// yet (0 = none).
func (st rollState) pending(inflight int64) int64 {
	switch {
	case st.Dirty == 0:
		return inflight
	case inflight == 0:
		return st.Dirty
	}
	return min(st.Dirty, inflight)
}

// saveRoll persists the rollup state. A range that is being rolled up right now is stored as
// dirty, so a crash in the middle of a Rollup redoes it after the restart. Callers hold rmu.
func (s *Store) saveRoll() error {
	st := s.roll
	st.Dirty = st.pending(s.inflight)
	return s.SetSetting(rollKey, st)
}

func floorTo(t, res int64) int64 {
	if t < 0 {
		return 0
	}
	return t - t%res
}

// markDirty records that samples at ts were written after their bucket was rolled up.
// It runs after the samples are committed, so a concurrent Rollup either sees the samples
// or the dirty mark.
func (s *Store) markDirty(ts int64) error {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	if s.roll.Five == 0 || ts >= s.roll.Five || (s.roll.Dirty != 0 && s.roll.Dirty <= ts) {
		return nil
	}
	s.roll.Dirty = ts
	return s.saveRoll()
}

// Rollup aggregates raw samples into 5-minute buckets and those into hourly buckets
// (min, max, average weighted by count, count). It only processes complete buckets since the
// last call plus buckets that received late samples, so it is cheap and safe to call every
// minute; running it again for the same time changes nothing.
func (s *Store) Rollup(now int64) error {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	s.rmu.Lock()
	st := s.roll
	start5 := st.Five
	if start5 == 0 {
		first, err := s.firstSample()
		if err != nil || first == 0 {
			s.rmu.Unlock()
			return err
		}
		start5 = floorTo(first, Res5m)
	}
	if st.Dirty != 0 {
		start5 = min(start5, floorTo(st.Dirty, Res5m))
	}
	end5 := floorTo(now-rollGrace, Res5m)
	newFive := max(st.Five, end5)
	start1 := floorTo(start5, Res1h)
	if st.Hour != 0 {
		start1 = min(start1, st.Hour)
	}
	end1 := floorTo(newFive, Res1h)
	// Claim the range before working: samples written below newFive from now on are dirty.
	s.roll.Five, s.roll.Dirty = newFive, 0
	s.inflight = start5
	s.rmu.Unlock()

	// CROSS JOIN keeps series as the outer loop, so samples/rollups are read by primary-key
	// range per series instead of by a full scan. The 5-minute range ends at newFive, not end5:
	// a dirty range above end5 (clock set back) must be redone as well.
	err := s.rollRange(start5, newFive, rollChunk5, `INSERT OR REPLACE INTO rollups (series_id, res, ts, min, max, avg, cnt)
		SELECT s.series_id, 300, (s.ts / 300) * 300, MIN(s.value), MAX(s.value), AVG(s.value), COUNT(*)
		FROM series r CROSS JOIN samples s ON s.series_id = r.id
		WHERE r.last_ts >= ?1 AND s.ts >= ?1 AND s.ts < ?2
		GROUP BY s.series_id, s.ts / 300`)
	if err == nil {
		err = s.rollRange(start1, end1, rollChunk1, `INSERT OR REPLACE INTO rollups (series_id, res, ts, min, max, avg, cnt)
			SELECT u.series_id, 3600, (u.ts / 3600) * 3600, MIN(u.min), MAX(u.max), SUM(u.avg * u.cnt) / SUM(u.cnt), SUM(u.cnt)
			FROM series r CROSS JOIN rollups u ON u.series_id = r.id AND u.res = 300
			WHERE r.last_ts >= ?1 AND u.ts >= ?1 AND u.ts < ?2
			GROUP BY u.series_id, u.ts / 3600`)
	}

	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.inflight = 0
	if err != nil {
		// Redo the whole range next time.
		if s.roll.Dirty == 0 || s.roll.Dirty > start5 {
			s.roll.Dirty = start5
		}
		if perr := s.saveRoll(); perr != nil {
			return fmt.Errorf("rollup: %w (state: %v)", err, perr)
		}
		return fmt.Errorf("rollup: %w", err)
	}
	s.roll.Hour = max(s.roll.Hour, end1)
	s.done.Five = max(s.done.Five, newFive)
	s.done.Hour = max(s.done.Hour, end1)
	return s.saveRoll()
}

// rollRange runs an aggregation statement (parameters ?1 = from, ?2 = to) over [from, to)
// in chunks, each under the write lock.
func (s *Store) rollRange(from, to, chunk int64, q string) error {
	for a := from; a < to; a += chunk {
		b := min(a+chunk, to)
		s.wmu.Lock()
		_, err := s.db.Exec(q, a, b)
		s.wmu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// firstSample returns the time of the oldest raw sample (0 = none). The correlated subquery
// reads one primary-key entry per series instead of scanning all samples.
func (s *Store) firstSample() (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRow("SELECT MIN((SELECT MIN(ts) FROM samples WHERE series_id = r.id)) FROM series r").Scan(&ts)
	return ts.Int64, err
}
