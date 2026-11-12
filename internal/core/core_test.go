package core

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

// ---- helpers ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type recorder struct {
	mu   sync.Mutex
	msgs []notify.Message
	err  error
}

func (r *recorder) send(_ context.Context, _ notify.Settings, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return r.err
}

func (r *recorder) titles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m.Title)
	}
	return out
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.msgs = nil
	r.mu.Unlock()
}

type env struct {
	c   *Core
	st  *store.Store
	clk *clock
	rec *recorder
}

func newEnv(t *testing.T, opt Options) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if opt.AgentDir == "" {
		opt.AgentDir = t.TempDir()
	}
	if opt.Version == "" {
		opt.Version = "1.2.0"
	}
	c, err := New(st, opt)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)}
	rec := &recorder{}
	c.Now, c.Notify = clk.Now, rec.send
	c.settings.Notify.HookEnabled, c.settings.Notify.HookURL = true, "http://127.0.0.1:1/hook"
	c.settings.General.Instance = "Testkunde"
	return &env{c: c, st: st, clk: clk, rec: rec}
}

func (e *env) wait() { e.c.notifyWG.Wait() }

func ptr(v float64) *float64 { return &v }

func metricCheck(key, name string, ms ...model.Metric) model.Check {
	return model.Check{Key: key, Name: name, Group: "System", State: model.StateOK, Metrics: ms}
}

func findCheck(st model.DeviceStatus, key string) (model.CheckStatus, bool) {
	for _, cs := range st.Checks {
		if cs.Key == key {
			return cs, true
		}
	}
	return model.CheckStatus{}, false
}

// agentDevice imports an agent device and returns its id.
func (e *env) agentDevice(t *testing.T, id string, mut func(*model.Device)) string {
	t.Helper()
	d := model.Device{ID: id, Name: id, Type: model.TypeAgent, Host: "10.0.0.9", Enabled: true,
		Created: e.clk.Now().Unix(), Agent: &model.AgentConfig{AgentID: id + "-m", OS: "linux", Arch: "amd64"}}
	if mut != nil {
		mut(&d)
	}
	if err := e.c.ImportDevice(d); err != nil {
		t.Fatal(err)
	}
	return id
}

// report sends a current report with the given checks.
func (e *env) report(t *testing.T, id string, checks ...model.Check) model.AgentReply {
	t.Helper()
	r, err := e.c.IngestReport(id, model.AgentReport{OS: "linux", Arch: "amd64", Version: "1.2.0",
		Time: e.clk.Now().Unix(), Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func diskCheck(pct float64) model.Check {
	return metricCheck("disk:/", "Festplatte /", model.Metric{Name: "disk.usage", Instance: "/", Value: pct, Unit: "%"})
}

// ---- evaluation ----

func TestThresholdPrecedence(t *testing.T) {
	m := model.Metric{Name: "disk.usage", Instance: "/var", Value: 50}
	dev := []model.Threshold{{Metric: "disk.usage", Warn: ptr(10)}, {Metric: "disk.usage:/var", Warn: ptr(20)}}
	glob := []model.Threshold{{Metric: "disk.usage:/var", Warn: ptr(30)}, {Metric: "disk.usage", Warn: ptr(40)}}
	def := model.DefaultThresholds()
	cases := []struct {
		name  string
		lists [][]model.Threshold
		want  float64
	}{
		{"device instance", [][]model.Threshold{dev, glob, def}, 20},
		{"device name", [][]model.Threshold{dev[:1], glob, def}, 10},
		{"global instance", [][]model.Threshold{nil, glob, def}, 30},
		{"global name", [][]model.Threshold{nil, glob[1:], def}, 40},
		{"default", [][]model.Threshold{nil, nil, def}, 85},
	}
	for _, tc := range cases {
		th, ok := findThreshold(m, tc.lists...)
		if !ok || *th.Warn != tc.want {
			t.Errorf("%s: got %v %v, want warn %v", tc.name, ok, th.Warn, tc.want)
		}
	}
	// Disabled at a higher level switches the metric off.
	off := []model.Threshold{{Metric: "disk.usage", Disabled: true}}
	if _, ok := findThreshold(m, off, glob, def); ok {
		t.Error("disabled device threshold should switch off")
	}
	// Instance-specific threshold does not match another instance.
	if th, _ := findThreshold(model.Metric{Name: "disk.usage", Instance: "/"}, dev[1:], nil, def); *th.Warn != 85 {
		t.Errorf("other instance: got %v", *th.Warn)
	}
}

func TestEvaluate(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "a1", func(d *model.Device) {
		d.Thresholds = []model.Threshold{
			{Metric: "ups.battery", Warn: ptr(50), Crit: ptr(20), Below: true},
			{Metric: "temp", Warn: ptr(40), Count: 3},
			{Metric: "swap.usage", Disabled: true},
		}
	})
	status := func() model.DeviceStatus { return e.c.Status(id) }
	get := func(key string) model.CheckStatus {
		cs, ok := findCheck(status(), key)
		if !ok {
			t.Fatalf("check %s missing", key)
		}
		return cs
	}

	// Reason text with label, instance and unit; device state and message.
	e.report(t, id, diskCheck(91))
	cs := get("disk:/")
	if cs.State != model.StateWarn || cs.Native != model.StateOK || cs.Reason != "Festplattenbelegung / 91 % ≥ 85 %" {
		t.Fatalf("disk: %+v", cs)
	}
	if st := status(); st.State != model.StateWarn || st.Message != "Festplatte /: Festplattenbelegung / 91 % ≥ 85 %" {
		t.Fatalf("device: %q %q", st.State, st.Message)
	}

	// Below.
	e.report(t, id, metricCheck("snmp:ups", "USV", model.Metric{Name: "ups.battery", Value: 15, Unit: "%"}))
	if cs := get("snmp:ups"); cs.State != model.StateCrit || cs.Reason != "Akku 15 % ≤ 20 %" {
		t.Fatalf("below: %+v", cs)
	}

	// Count hysteresis: 3 bad values needed, ok is immediate.
	temp := func(v float64) model.Check {
		return metricCheck("temp", "Temperatur", model.Metric{Name: "temp", Instance: "CPU", Value: v, Unit: "°C"})
	}
	for i, want := range []struct {
		v       float64
		state   string
		pending int
	}{{45, "ok", 1}, {46, "ok", 2}, {47, "warn", 0}, {48, "warn", 0}, {30, "ok", 0}, {45, "ok", 1}} {
		e.report(t, id, temp(want.v))
		cs := get("temp")
		if cs.State != want.state || cs.Pending != want.pending {
			t.Fatalf("step %d: state %s pending %d, want %s %d", i, cs.State, cs.Pending, want.state, want.pending)
		}
	}

	// Disabled threshold; native state wins over thresholds.
	e.report(t, id, model.Check{Key: "swap", Name: "Auslagerung", State: model.StateOK,
		Metrics: []model.Metric{{Name: "swap.usage", Value: 99, Unit: "%"}}})
	if cs := get("swap"); cs.State != model.StateOK {
		t.Fatalf("disabled: %+v", cs)
	}
	e.report(t, id, model.Check{Key: "svc:x", Name: "Dienst x", State: model.StateCrit, Message: "gestoppt"})
	if cs := get("svc:x"); cs.State != model.StateCrit || cs.Reason != "gestoppt" {
		t.Fatalf("native: %+v", cs)
	}

	// CPU default count 5.
	cpu := metricCheck("cpu", "CPU", model.Metric{Name: "cpu.usage", Value: 99, Unit: "%"})
	for i := range 4 {
		e.report(t, id, cpu)
		if cs := get("cpu"); cs.State != model.StateOK || cs.Pending != i+1 {
			t.Fatalf("cpu %d: %+v", i, cs)
		}
	}
	e.report(t, id, cpu)
	if cs := get("cpu"); cs.State != model.StateCrit || cs.Reason != "CPU-Auslastung 99 % ≥ 98 %" {
		t.Fatalf("cpu: %+v", cs)
	}
}

func TestDeviceState(t *testing.T) {
	cs := func(key, state, reason string) model.CheckStatus {
		return model.CheckStatus{Check: model.Check{Key: key, Name: strings.ToUpper(key), State: state, Message: "m-" + key}, Reason: reason}
	}
	cases := []struct {
		checks       []model.CheckStatus
		state, msgIs string
	}{
		{nil, model.StateUnknown, "Keine Daten"},
		{[]model.CheckStatus{cs("a", "unknown", "")}, model.StateUnknown, "m-a"},
		{[]model.CheckStatus{cs("a", "unknown", ""), cs("b", "ok", "")}, model.StateOK, ""},
		{[]model.CheckStatus{cs("a", "unknown", ""), cs("b", "warn", "zu voll")}, model.StateWarn, "B: zu voll"},
		{[]model.CheckStatus{cs("a", "warn", "w"), cs("b", "crit", "c")}, model.StateCrit, "2 Probleme: B: c; A: w"},
		{[]model.CheckStatus{cs("reach", "crit", "Timeout"), cs("b", "unknown", "")}, model.StateCrit, "Timeout"},
	}
	for i, tc := range cases {
		st, msg := deviceState(tc.checks)
		if st != tc.state || msg != tc.msgIs {
			t.Errorf("%d: got %s %q, want %s %q", i, st, msg, tc.state, tc.msgIs)
		}
	}
}

// ---- alerts ----

func TestAlertLifecycle(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "web01", nil)
	e.report(t, id, diskCheck(50))
	e.wait()
	if len(e.rec.titles()) != 0 {
		t.Fatalf("no notification expected: %v", e.rec.titles())
	}

	// Open.
	e.report(t, id, diskCheck(91))
	e.wait()
	if got := e.rec.titles(); !slices.Equal(got, []string{"Warnung: web01 – Festplatte /"}) {
		t.Fatalf("open: %v", got)
	}
	m := e.rec.msgs[0]
	if m.Severity != "warning" || m.Host != "Testkunde" || m.Source != "web01" || m.Body != "Festplattenbelegung / 91 % ≥ 85 %" ||
		m.Details == nil || m.Details.Link != "/devices/web01" {
		t.Fatalf("message: %+v", m)
	}
	facts := map[string]string{}
	for _, f := range m.Details.Facts {
		facts[f.Label] = f.Value
	}
	if facts["Wert"] != "91 %" || facts["Schwelle"] != "≥ 85 %" || facts["Check"] != "Festplatte /" || facts["seit"] == "" {
		t.Fatalf("facts: %v", facts)
	}
	active, _ := e.st.ActiveAlerts()
	if len(active) != 1 || active[0].Severity != model.StateWarn {
		t.Fatalf("active: %+v", active)
	}

	// Same state again: nothing new. Escalation: notify.
	e.report(t, id, diskCheck(92))
	e.report(t, id, diskCheck(97))
	e.wait()
	if got := e.rec.titles(); len(got) != 2 || got[1] != "Kritisch: web01 – Festplatte /" {
		t.Fatalf("escalate: %v", got)
	}
	// De-escalation updates the alert without notification.
	e.report(t, id, diskCheck(90))
	e.wait()
	active, _ = e.st.ActiveAlerts()
	if len(e.rec.titles()) != 2 || len(active) != 1 || active[0].Severity != model.StateWarn {
		t.Fatalf("de-escalate: %v %+v", e.rec.titles(), active)
	}

	// Re-notify every N hours unless acknowledged.
	e.c.settings.General.RenotifyHours = 1
	e.clk.Add(30 * time.Minute)
	e.c.renotify()
	e.clk.Add(31 * time.Minute)
	e.c.renotify()
	e.wait()
	if got := e.rec.titles(); len(got) != 3 || got[2] != "Weiterhin Warnung: web01 – Festplatte /" {
		t.Fatalf("renotify: %v", got)
	}
	if err := e.c.AckAlert(active[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	e.clk.Add(2 * time.Hour)
	e.c.renotify()
	e.wait()
	if len(e.rec.titles()) != 3 {
		t.Fatalf("acked alert re-notified: %v", e.rec.titles())
	}

	// Close: "Wieder OK".
	e.report(t, id, diskCheck(40))
	e.wait()
	if got := e.rec.titles(); len(got) != 4 || got[3] != "Wieder OK: web01 – Festplatte /" || e.rec.msgs[3].Severity != "info" {
		t.Fatalf("close: %v", got)
	}
	if active, _ = e.st.ActiveAlerts(); len(active) != 0 {
		t.Fatalf("still active: %+v", active)
	}
}

func TestAlertsMaintenanceAndRemoval(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "fs01", nil)
	svc := model.Check{Key: "svc:Spooler", Name: "Dienst Druckwarteschlange", State: model.StateCrit, Message: "beendet"}
	e.report(t, id, svc, diskCheck(90))
	e.wait()
	if len(e.rec.titles()) != 2 {
		t.Fatalf("open: %v", e.rec.titles())
	}
	e.rec.reset()

	// A check that disappears closes silently.
	e.report(t, id, svc)
	e.wait()
	active, _ := e.st.ActiveAlerts()
	if len(active) != 1 || active[0].CheckKey != "svc:Spooler" || len(e.rec.titles()) != 0 {
		t.Fatalf("removed: %+v %v", active, e.rec.titles())
	}

	// Maintenance closes silently and raises nothing.
	if _, err := e.c.SetMaintenance(id, e.clk.Now().Add(time.Hour).Unix(), "Update"); err != nil {
		t.Fatal(err)
	}
	e.report(t, id, svc, diskCheck(99))
	e.wait()
	if active, _ = e.st.ActiveAlerts(); len(active) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("maintenance: %+v %v", active, e.rec.titles())
	}
	// Maintenance ends by time: the next sync opens the alerts again.
	e.clk.Add(2 * time.Hour)
	e.c.syncAllAlerts()
	e.wait()
	if active, _ = e.st.ActiveAlerts(); len(active) != 2 || len(e.rec.titles()) != 2 {
		t.Fatalf("after maintenance: %+v %v", active, e.rec.titles())
	}
	// Deleting the device closes its alerts.
	if err := e.c.DeleteDevice(id); err != nil {
		t.Fatal(err)
	}
	if active, _ = e.st.ActiveAlerts(); len(active) != 0 {
		t.Fatalf("after delete: %+v", active)
	}
	if _, err := e.c.Device(id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device still there: %v", err)
	}
}

func TestNotifyFailureEventOnce(t *testing.T) {
	e := newEnv(t, Options{})
	e.rec.err = errors.New("smtp kaputt")
	for i := range 3 {
		e.c.Event("warning", "Test", "Fehler "+string(rune('A'+i)), "x", nil, true)
		e.wait()
	}
	evs, _ := e.st.Events(context.Background(), "warning", 0, 100)
	n := 0
	for _, ev := range evs {
		if ev.Title == "Benachrichtigung fehlgeschlagen" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one failure event, got %d", n)
	}
	// After a success the next failure is reported again.
	e.rec.err = nil
	e.c.Event("warning", "Test", "ok", "x", nil, true)
	e.wait()
	e.rec.err = errors.New("again")
	e.c.Event("warning", "Test", "fail", "x", nil, true)
	e.wait()
	evs, _ = e.st.Events(context.Background(), "warning", 0, 100)
	n = 0
	for _, ev := range evs {
		if ev.Title == "Benachrichtigung fehlgeschlagen" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want two failure events, got %d", n)
	}
}

func TestPersistAndReload(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "db01", nil)
	e.report(t, id, diskCheck(96))
	e.c.persist(true)
	c2, err := New(e.st, Options{AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	st := c2.Status(id)
	if st.State != model.StateCrit || len(c2.open) != 1 || c2.rt[id].thr["disk:/"] != model.StateCrit {
		t.Fatalf("reloaded: %+v open=%d thr=%v", st, len(c2.open), c2.rt[id].thr)
	}
}
