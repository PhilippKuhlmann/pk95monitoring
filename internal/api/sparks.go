package api

import (
	"cmp"
	"math"
	"net/http"
	"slices"
	"strconv"

	"pk95monitoring/internal/store"
)

// sparkRanges are the ranges of the sparklines on the device page (GET /api/devices/{id}/sparks).
var sparkRanges = map[string]int64{"1h": 3600, "6h": 6 * 3600, "24h": 24 * 3600}

const (
	sparkPoints    = 48  // buckets per sparkline (fewer when the poll interval is longer than span/48)
	maxSparkSeries = 600 // series per response; the most recently updated ones win
)

// sparkValues encodes bucket averages as a JSON array with null for empty buckets (NaN) and at
// most 6 significant digits, which is plenty for a sparkline and keeps 600 × 48 values small.
type sparkValues []float64

// MarshalJSON implements json.Marshaler.
func (v sparkValues) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 2+len(v)*8)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b = append(b, "null"...)
			continue
		}
		b = strconv.AppendFloat(b, f, 'g', 6, 64)
	}
	return append(b, ']'), nil
}

type sparkSeries struct {
	ID       int64       `json:"id"`
	CheckKey string      `json:"check_key"`
	Name     string      `json:"name"`
	Instance string      `json:"instance"`
	Unit     string      `json:"unit"`
	Last     *float64    `json:"last"`
	LastTS   int64       `json:"last_ts"`
	Values   sparkValues `json:"values"`
}

// sparkWindow returns the buckets of a sparkline ending at now: the bucket size (span / 48, at
// least the poll interval in whole minutes, so a slowly polled device has no gap between every
// two points), the start of the first bucket (aligned to multiples of step, as store.Query
// aligns them) and the number of buckets. The last bucket contains now.
func sparkWindow(now, span int64, interval int) (from, step int64, n int) {
	step = metricStep((span+sparkPoints-1)/sparkPoints, interval)
	n = int((span + step - 1) / step)
	last := now / step * step
	return last - int64(n-1)*step, step, n
}

// sparkBuckets spreads the points of one series over n buckets starting at from; buckets
// without a point are NaN (encoded as null).
func sparkBuckets(pts []store.Point, from, step int64, n int) sparkValues {
	v := make(sparkValues, n)
	for i := range v {
		v[i] = math.NaN()
	}
	for _, p := range pts {
		if p.TS < from {
			continue
		}
		if i := (p.TS - from) / step; i < int64(n) {
			v[i] = p.Avg
		}
	}
	return v
}

// sparkSelect returns the series to show: all of them, or the maxSparkSeries most recently
// updated ones (capped = true). The order of list (name, instance) is kept.
func sparkSelect(list []store.Series) (out []store.Series, capped bool) {
	if len(list) <= maxSparkSeries {
		return list, false
	}
	byNew := slices.Clone(list)
	slices.SortStableFunc(byNew, func(a, b store.Series) int { return cmp.Compare(b.LastTS, a.LastTS) })
	keep := make(map[int64]bool, maxSparkSeries)
	for _, se := range byNew[:maxSparkSeries] {
		keep[se.ID] = true
	}
	out = make([]store.Series, 0, maxSparkSeries)
	for _, se := range list {
		if keep[se.ID] {
			out = append(out, se)
		}
	}
	return out, true
}

// sparks answers GET /api/devices/{id}/sparks?range=1h|6h|24h (default 6h): every series of the
// device with its latest value and about 48 bucket averages, for the overview of a device's
// checks. All series are read with one store query.
func (s *Server) sparks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.C.Device(id)
	if err != nil {
		fail(w, err)
		return
	}
	rg := r.URL.Query().Get("range")
	if rg == "" {
		rg = "6h"
	}
	span, okRange := sparkRanges[rg]
	if !okRange {
		writeErr(w, http.StatusBadRequest, "Unbekannter Zeitraum.")
		return
	}
	all, err := s.C.St.SeriesList(id)
	if err != nil {
		fail(w, err)
		return
	}
	list, capped := sparkSelect(all)
	to := s.C.Now().Unix()
	from, step, n := sparkWindow(to, span, d.PollInterval())
	ids := make([]int64, len(list))
	for i, se := range list {
		ids[i] = se.ID
	}
	pts, err := s.C.St.Query(ids, from, to, step)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]sparkSeries, 0, len(list))
	for _, se := range list {
		ss := sparkSeries{ID: se.ID, CheckKey: se.CheckKey, Name: se.Name, Instance: se.Instance, Unit: se.Unit,
			LastTS: se.LastTS, Values: sparkBuckets(pts[se.ID], from, step, n)}
		if se.LastTS > 0 && !math.IsNaN(se.LastValue) && !math.IsInf(se.LastValue, 0) {
			last := se.LastValue
			ss.Last = &last
		}
		out = append(out, ss)
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "step": step, "capped": capped, "series": out})
}
