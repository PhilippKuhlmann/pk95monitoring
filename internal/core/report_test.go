package core

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestReport(t *testing.T) {
	e := newEnv(t, Options{})
	t0 := e.clk.Now().Unix()
	web := e.agentDevice(t, "web01", nil)
	e.agentDevice(t, "db01", nil)
	h := int64(3600)
	add := func(dev, key, name, sev string, from, to int64) {
		t.Helper()
		id, err := e.st.OpenAlert(model.Alert{DeviceID: dev, DeviceName: dev, CheckKey: key, CheckName: name, Severity: sev, Started: t0 + from})
		if err != nil {
			t.Fatal(err)
		}
		if to != 0 { // 0 = still open
			if err := e.st.CloseAlert(id, t0+to); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(web, reachKey, "Erreichbarkeit", model.StateCrit, 1*h, 2*h) // outage 1 h
	add(web, reachKey, "Erreichbarkeit", model.StateCrit, 9*h, 0)   // still down: 1 h until now
	add(web, "disk:/", "Festplatte /", model.StateWarn, 3*h, 5*h)   // problems 3–5 h …
	add(web, "cpu", "CPU", model.StateCrit, 4*h, 6*h)               // … and 4–6 h: 3 h together
	add(web, "cpu", "CPU", model.StateCrit, -5*h, -4*h)             // before the period
	e.clk.Add(10 * time.Hour)

	r, err := e.c.Report(context.Background(), t0-5*h, t0+20*h) // starts before the devices, ends after now
	if err != nil {
		t.Fatal(err)
	}
	if r.To != t0+10*h || len(r.Devices) != 2 {
		t.Fatalf("report %+v", r)
	}
	d := r.Devices[0] // least available first
	if d.ID != web || d.Covered != 10*h || d.Downtime != 2*h || d.Outages != 2 || d.Longest != h || d.Alerts != 4 || d.ProblemTime != 3*h {
		t.Fatalf("web01 %+v", d)
	}
	if math.Abs(d.Availability-80) > 1e-9 {
		t.Fatalf("availability %v", d.Availability)
	}
	if len(d.Checks) != 2 || d.Checks[0].Key != "cpu" || d.Checks[0].Crit != 2*h || d.Checks[1].Key != "disk:/" || d.Checks[1].Warn != 2*h || d.Checks[1].Longest != 2*h {
		t.Fatalf("checks %+v", d.Checks)
	}
	if db := r.Devices[1]; db.Availability != 100 || db.Alerts != 0 || len(db.Checks) != 0 {
		t.Fatalf("db01 %+v", db)
	}
	if r.Availability == nil || math.Abs(*r.Availability-90) > 1e-9 || r.Outages != 2 || r.Downtime != 2*h || r.Alerts != 4 {
		t.Fatalf("totals %+v", r)
	}

	// A device created after the period is left out; bad periods are errors.
	if r, _ := e.c.Report(context.Background(), t0-5*h, t0-h); len(r.Devices) != 0 || r.Availability != nil {
		t.Fatalf("before creation %+v", r)
	}
	for _, p := range [][2]int64{{t0, t0}, {t0 + h, t0}, {t0 - 401*24*h, t0}} {
		if _, err := e.c.Report(context.Background(), p[0], p[1]); !errors.Is(err, ErrReportPeriod) {
			t.Errorf("period %v: %v", p, err)
		}
	}
}
