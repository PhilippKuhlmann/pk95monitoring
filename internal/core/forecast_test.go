package core

import (
	"math"
	"strings"
	"testing"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// levels are hourly fill levels over days with a daily swing.
func levels(days int, start, perDay float64) []store.Point {
	var pts []store.Point
	for h := 0; h <= days*24; h++ {
		v := start + perDay*float64(h)/24 + 0.8*math.Sin(float64(h)/24*2*math.Pi)
		pts = append(pts, store.Point{TS: int64(1_700_000_000 + h*3600), Avg: v})
	}
	return pts
}

func TestGrowthRate(t *testing.T) {
	if r, ok := growthRate(levels(7, 60, 1.5)); !ok || math.Abs(r-1.5) > 0.1 {
		t.Errorf("growing: %v %v", r, ok)
	}
	if _, ok := growthRate(levels(7, 60, 0)); ok {
		t.Error("stable disk has a forecast")
	}
	if _, ok := growthRate(levels(7, 60, -0.5)); ok {
		t.Error("shrinking disk has a forecast")
	}
	if _, ok := growthRate(levels(1, 60, 3)); ok {
		t.Error("one day of history is too little")
	}
	// a cleanup restarts the trend: only the growth after it counts
	shift := func(pts []store.Point, by int) []store.Point {
		for i := range pts {
			pts[i].TS += int64(by) * 3600
		}
		return pts
	}
	before := levels(5, 80, 2)
	if r, ok := growthRate(append(before, shift(levels(4, 50, 0.5), len(before))...)); !ok || math.Abs(r-0.5) > 0.1 {
		t.Errorf("after cleanup: %v %v", r, ok)
	}
	before = levels(5, 80, 2)
	if _, ok := growthRate(append(before, shift(levels(1, 50, 0.5), len(before))...)); ok {
		t.Error("too little history after the cleanup")
	}
}

// End to end: stored history → growth → forecast.days with the default threshold.
func TestForecast(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "fs", nil)
	now := e.clk.Now().Unix()
	disk := func(mount string, pct float64) model.Check {
		return model.Check{Key: "disk:" + mount, Name: "Festplatte " + mount, Group: "Festplatten", State: model.StateOK,
			Metrics: []model.Metric{{Name: "disk.usage", Instance: mount, Value: pct, Unit: "%"}}}
	}
	// 7 days: E: grows 2 % per day up to 80 %, C: stays at 40 %
	for ts := now - 7*86400; ts < now; ts += 1800 {
		age := float64(now-ts) / 86400
		if err := e.st.WriteMetrics(id, ts, []model.Check{disk("E:", 80-2*age), disk("C:", 40)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.c.UpdateForecasts(); err != nil {
		t.Fatal(err)
	}
	e.report(t, id, disk("E:", 80), disk("C:", 40))
	st := e.c.Status(id)
	cs, _ := findCheck(st, "disk:E:")
	days := math.NaN()
	for _, m := range cs.Metrics {
		if m.Name == "forecast.days" {
			days = m.Value
		}
	}
	if math.Abs(days-10) > 0.5 {
		t.Fatalf("forecast %v (%+v)", days, cs.Metrics)
	}
	if cs.State != model.StateWarn || !strings.Contains(cs.Reason, "Voll in") || cs.Facts["Zuwachs"] != "2 % pro Tag" || cs.Facts["Voll voraussichtlich"] == "" {
		t.Errorf("E: %+v", cs)
	}
	if c, _ := findCheck(st, "disk:C:"); len(c.Metrics) != 1 || c.State != model.StateOK {
		t.Errorf("C: %+v", c)
	}
	rows := e.c.Expiry()
	if len(rows) != 1 || rows[0].Kind != "storage" || rows[0].CheckKey != "disk:E:" || math.Abs(*rows[0].Days-10) > 0.5 ||
		rows[0].Until == "" || !strings.Contains(rows[0].Detail, "2 % pro Tag") {
		t.Fatalf("expiry %+v", rows)
	}
}
