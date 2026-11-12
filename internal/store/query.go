package store

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Ranges up to these spans are answered from raw samples or 5-minute rollups.
const (
	rawSpan  = 48 * 3600
	fiveSpan = 35 * 86400
)

// Point is one bucket of a queried series. It is encoded as [ts, avg, min, max].
type Point struct {
	TS            int64
	Avg, Min, Max float64
}

// MarshalJSON encodes the point as a compact array.
func (p Point) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 48)
	b = append(b, '[')
	b = strconv.AppendInt(b, p.TS, 10)
	for _, v := range [3]float64{p.Avg, p.Min, p.Max} {
		b = append(b, ',')
		b = appendJSONFloat(b, v)
	}
	return append(b, ']'), nil
}

// UnmarshalJSON decodes [ts, avg, min, max].
func (p *Point) UnmarshalJSON(data []byte) error {
	var a [4]*float64
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	if a[0] == nil {
		return fmt.Errorf("point without time: %s", data)
	}
	val := func(f *float64) float64 {
		if f == nil {
			return math.NaN()
		}
		return *f
	}
	*p = Point{TS: int64(*a[0]), Avg: val(a[1]), Min: val(a[2]), Max: val(a[3])}
	return nil
}

func appendJSONFloat(b []byte, v float64) []byte {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return append(b, "null"...)
	}
	if abs := math.Abs(v); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		return strconv.AppendFloat(b, v, 'e', -1, 64)
	}
	return strconv.AppendFloat(b, v, 'f', -1, 64)
}

// sourceRes chooses the data source for a time span: 0 (raw), Res5m or Res1h.
func sourceRes(span int64) int64 {
	switch {
	case span <= rawSpan:
		return 0
	case span <= fiveSpan:
		return Res5m
	}
	return Res1h
}

// Query returns the values of the series between from and to (unix seconds, inclusive),
// aggregated into buckets of step seconds (aligned to multiples of step) with average, minimum
// and maximum. Spans up to 48 h are read from raw samples, up to 35 days from 5-minute rollups,
// longer ones from hourly rollups; the step is at least the source resolution. The part that is
// not rolled up yet is filled from finer data, the part beyond a shorter retention from coarser
// data. Empty buckets are omitted; every requested series is present in the result.
func (s *Store) Query(seriesIDs []int64, from, to, step int64) (map[int64][]Point, error) {
	out := make(map[int64][]Point, len(seriesIDs))
	ids := make([]any, 0, len(seriesIDs))
	for _, id := range seriesIDs {
		if _, ok := out[id]; !ok {
			out[id] = []Point{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || to < from {
		return out, nil
	}
	res := sourceRes(to - from)
	step = max(step, res, 1)

	s.rmu.Lock()
	done := s.done
	s.rmu.Unlock()

	in := "series_id IN (?" + strings.Repeat(", ?", len(ids)-1) + ")"
	var parts []string
	var args []any
	rollPart := func(r, lo, hi int64) {
		if lo >= hi {
			return
		}
		parts = append(parts, "SELECT series_id, (ts / ?) * ? AS b, avg * cnt AS sm, cnt AS c, min AS mn, max AS mx FROM rollups WHERE "+
			in+" AND res = ? AND ts >= ? AND ts < ?")
		args = append(args, step, step)
		args = append(args, ids...)
		args = append(args, r, lo, hi)
	}
	// Hourly rollups answer [from, hour), 5-minute rollups [hour, five), raw samples [five, to].
	// Parts that are not rolled up yet come from finer data, parts that are pruned already
	// (retention shorter than the range) from coarser data.
	var hour, five int64
	switch res {
	case Res1h:
		hour, five = done.Hour, done.Five
	case Res5m:
		hour, five = min(done.Hour, done.FiveCut), done.Five
	default:
		hour, five = min(done.Hour, done.FiveCut), min(done.Five, done.RawCut)
	}
	five = max(five, hour)
	rollPart(Res1h, from, min(hour, to+1))
	rollPart(Res5m, max(from, hour), min(five, to+1))
	rawFrom := max(from, five)
	parts = append(parts, "SELECT series_id, (ts / ?) * ? AS b, value AS sm, 1 AS c, value AS mn, value AS mx FROM samples WHERE "+
		in+" AND ts >= ? AND ts <= ?")
	args = append(args, step, step)
	args = append(args, ids...)
	args = append(args, rawFrom, to)

	q := "SELECT series_id, b, SUM(sm) / SUM(c), MIN(mn), MAX(mx) FROM (" + strings.Join(parts, " UNION ALL ") +
		") GROUP BY series_id, b ORDER BY series_id, b"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p Point
		if err := rows.Scan(&id, &p.TS, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}
