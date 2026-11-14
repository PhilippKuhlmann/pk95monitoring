package core

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
)

func diskAt(mount string, pct float64) model.Check {
	return model.Check{Key: "disk:" + mount, Name: "Festplatte " + mount, Group: "Festplatten", State: model.StateOK,
		Metrics: []model.Metric{{Name: "disk.usage", Instance: mount, Value: pct, Unit: "%"}}}
}

func cpuAt(pct float64) model.Check {
	return metricCheck("cpu", "CPU", model.Metric{Name: "cpu.usage", Value: pct, Unit: "%"})
}

func seriesInstances(e *env, id, name string) []string {
	list, _ := e.st.SeriesList(id)
	var out []string
	for _, s := range list {
		if s.Name == name {
			out = append(out, s.Instance)
		}
	}
	slices.Sort(out)
	return out
}

// setRules saves the device with new rules through SaveDevice (as the UI does).
func (e *env) setRules(t *testing.T, id string, rules ...model.CheckRule) model.Device {
	t.Helper()
	d, err := e.c.Device(id)
	if err != nil {
		t.Fatal(err)
	}
	d.Rules = rules
	saved, fields, err := e.c.SaveDevice(d)
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	return saved
}

func activeKeys(e *env) []string {
	list, _ := e.st.ActiveAlerts()
	var out []string
	for _, a := range list {
		out = append(out, a.CheckKey)
	}
	slices.Sort(out)
	return out
}

func lastEventBody(t *testing.T, e *env, title string) string {
	t.Helper()
	evs, err := e.st.Events(context.Background(), "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Title == title {
			return ev.Body
		}
	}
	return ""
}

func TestRuleOffDropsCheckAndMetrics(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "web01", func(d *model.Device) {
		d.Rules = []model.CheckRule{{Match: "disk:/boot*", Mode: model.ModeOff}}
	})
	e.report(t, id, diskAt("/", 40), diskAt("/boot", 99), diskAt("/boot/efi", 99))
	st := e.c.Status(id)
	if _, ok := findCheck(st, "disk:/boot"); ok {
		t.Fatalf("off check shown: %+v", st.Checks)
	}
	want := []model.HiddenCheck{
		{Key: "disk:/boot", Name: "Festplatte /boot", Group: "Festplatten"},
		{Key: "disk:/boot/efi", Name: "Festplatte /boot/efi", Group: "Festplatten"},
	}
	if !slices.Equal(st.Hidden, want) || st.State != model.StateOK {
		t.Fatalf("hidden: %+v state %s", st.Hidden, st.State)
	}
	if got := seriesInstances(e, id, "disk.usage"); !slices.Equal(got, []string{"/"}) {
		t.Fatalf("series: %v", got)
	}
	if n := countChecks(st.Checks); n.OK != 3 || n.Warn+n.Crit+n.Unknown != 0 { // reach, disk:/, discovery
		t.Fatalf("counts: %+v", n)
	}

	// A buffered report stores no metrics of off checks either.
	if _, err := e.c.IngestReport(id, model.AgentReport{Buffered: true, Time: e.clk.Now().Unix() - 600,
		Checks: []model.Check{diskAt("/", 41), diskAt("/boot", 98), diskAt("/data", 10)}}); err != nil {
		t.Fatal(err)
	}
	if got := seriesInstances(e, id, "disk.usage"); !slices.Equal(got, []string{"/", "/data"}) {
		t.Fatalf("buffered series: %v", got)
	}
	if st := e.c.Status(id); len(st.Hidden) != 2 {
		t.Fatalf("buffered report changed hidden: %+v", st.Hidden)
	}

	// Showing it again drops it from Hidden at once; the check returns with the next report.
	e.setRules(t, id)
	if st := e.c.Status(id); len(st.Hidden) != 0 {
		t.Fatalf("hidden after un-hide: %+v", st.Hidden)
	}
	e.report(t, id, diskAt("/", 40), diskAt("/boot", 50))
	if _, ok := findCheck(e.c.Status(id), "disk:/boot"); !ok {
		t.Fatal("un-hidden check missing after report")
	}
}

func TestRuleQuiet(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "db01", func(d *model.Device) {
		d.Rules = []model.CheckRule{{Match: "updates", Mode: model.ModeQuiet}, {Match: "disk:/", Mode: model.ModeQuiet}}
	})
	upd := model.Check{Key: "updates", Name: "Updates", Group: "Updates", State: model.StateWarn, Message: "Neustart erforderlich",
		Metrics: []model.Metric{{Name: "updates.security", Value: 3}}}
	e.report(t, id, upd, diskAt("/", 97), cpuAt(10))
	e.wait()
	st := e.c.Status(id)
	u, _ := findCheck(st, "updates")
	if u.State != model.StateOK || u.Muted != model.StateWarn || u.Mode != model.ModeQuiet || u.Native != model.StateWarn || u.Reason == "" {
		t.Fatalf("updates: %+v", u)
	}
	dk, _ := findCheck(st, "disk:/")
	if dk.State != model.StateOK || dk.Muted != model.StateCrit || dk.Reason != "Festplattenbelegung / 97 % ≥ 95 %" {
		t.Fatalf("disk: %+v", dk)
	}
	if c, _ := findCheck(st, "cpu"); c.Mode != "" || c.Muted != "" {
		t.Fatalf("cpu: %+v", c)
	}
	if st.State != model.StateOK || st.Message != "" {
		t.Fatalf("device: %s %q", st.State, st.Message)
	}
	if got := activeKeys(e); len(got) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("alerts: %v %v", got, e.rec.titles())
	}
	if !hasMetric(e, id, "disk.usage") || !hasMetric(e, id, "updates.security") {
		t.Fatal("quiet metrics not stored")
	}
	rows := e.c.Rows()
	// the updates check is separate (own page): not counted with the device's checks
	if rows[0].State != model.StateOK || rows[0].Checks.OK != 4 || rows[0].Checks.Warn+rows[0].Checks.Crit != 0 {
		t.Fatalf("row: %+v", rows[0])
	}
	if sum := e.c.Summary(); sum.State != model.StateOK || sum.Devices[0].Checks["ok"] != 4 {
		t.Fatalf("summary: %+v", sum)
	}

	// Quiet unknown stays unknown but does not colour the device.
	upd.State, upd.Metrics = model.StateUnknown, nil
	e.report(t, id, upd, diskAt("/", 50))
	st = e.c.Status(id)
	if u, _ = findCheck(st, "updates"); u.State != model.StateUnknown || u.Muted != "" || st.State != model.StateOK {
		t.Fatalf("unknown quiet: %+v device %s", u, st.State)
	}
}

func TestRuleSwitchAlertQuietAlert(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "web01", nil)
	e.report(t, id, diskAt("/", 91))
	e.wait()
	if got := activeKeys(e); !slices.Equal(got, []string{"disk:/"}) {
		t.Fatalf("open: %v", got)
	}
	e.rec.reset()

	// alert → quiet: the alert closes silently, the status changes without a poll.
	e.setRules(t, id, model.CheckRule{Match: "disk:*", Mode: model.ModeQuiet})
	e.wait()
	st := e.c.Status(id)
	dk, _ := findCheck(st, "disk:/")
	if dk.State != model.StateOK || dk.Muted != model.StateWarn || dk.Mode != model.ModeQuiet || st.State != model.StateOK {
		t.Fatalf("quiet: %+v device %s", dk, st.State)
	}
	if got := activeKeys(e); len(got) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("after quiet: %v %v", got, e.rec.titles())
	}
	if body := lastEventBody(t, e, "Alarm beendet: web01 – Festplatte /"); !strings.Contains(body, "wird nicht mehr alarmiert") {
		t.Fatalf("event body %q", body)
	}
	// Still quiet on the next report.
	e.report(t, id, diskAt("/", 92))
	e.wait()
	if got := activeKeys(e); len(got) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("quiet report: %v %v", got, e.rec.titles())
	}

	// quiet → alert: still bad, so the alert opens again at once.
	e.setRules(t, id)
	e.wait()
	if dk, _ = findCheck(e.c.Status(id), "disk:/"); dk.State != model.StateWarn || dk.Mode != "" || dk.Muted != "" {
		t.Fatalf("alert again: %+v", dk)
	}
	if got := activeKeys(e); !slices.Equal(got, []string{"disk:/"}) || !slices.Equal(e.rec.titles(), []string{"Warnung: web01 – Festplatte /"}) {
		t.Fatalf("re-open: %v %v", got, e.rec.titles())
	}
	e.rec.reset()

	// alert → off: the check is hidden and its alert closes silently.
	e.setRules(t, id, model.CheckRule{Match: "disk:/", Mode: model.ModeOff})
	e.wait()
	st = e.c.Status(id)
	if _, ok := findCheck(st, "disk:/"); ok || len(st.Hidden) != 1 || st.Hidden[0].Key != "disk:/" {
		t.Fatalf("off: %+v hidden %+v", st.Checks, st.Hidden)
	}
	if got := activeKeys(e); len(got) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("after off: %v %v", got, e.rec.titles())
	}
	if body := lastEventBody(t, e, "Alarm beendet: web01 – Festplatte /"); !strings.Contains(body, "ausgeblendet") {
		t.Fatalf("event body %q", body)
	}
}

func TestRuleQuietKeepsHysteresis(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "app01", func(d *model.Device) {
		d.Rules = []model.CheckRule{{Match: "cpu", Mode: model.ModeQuiet}}
	})
	// cpu.usage: crit ≥ 98 after 5 values.
	for range 2 {
		e.report(t, id, cpuAt(99))
		e.clk.Add(time.Minute)
	}
	c, _ := findCheck(e.c.Status(id), "cpu")
	if c.State != model.StateOK || c.Muted != "" || c.Pending != 2 {
		t.Fatalf("pending quiet: %+v", c)
	}
	// Back to alert: the count goes on, it does not start again.
	e.setRules(t, id)
	if c, _ = findCheck(e.c.Status(id), "cpu"); c.State != model.StateOK || c.Pending != 2 {
		t.Fatalf("after switch: %+v", c)
	}
	for range 2 {
		e.report(t, id, cpuAt(99))
		e.clk.Add(time.Minute)
	}
	if c, _ = findCheck(e.c.Status(id), "cpu"); c.State != model.StateOK || c.Pending != 4 {
		t.Fatalf("4th value: %+v", c)
	}
	e.report(t, id, cpuAt(99))
	e.wait()
	if c, _ = findCheck(e.c.Status(id), "cpu"); c.State != model.StateCrit {
		t.Fatalf("5th value: %+v", c)
	}
	if got := activeKeys(e); !slices.Equal(got, []string{"cpu"}) {
		t.Fatalf("alerts: %v", got)
	}
	// And quiet again keeps the verdict in Muted; the reload restores it as threshold state.
	e.setRules(t, id, model.CheckRule{Match: "cpu", Mode: model.ModeQuiet})
	if c, _ = findCheck(e.c.Status(id), "cpu"); c.State != model.StateOK || c.Muted != model.StateCrit {
		t.Fatalf("quiet again: %+v", c)
	}
	e.c.persist(true)
	c2, err := New(e.st, e.c.Opt)
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.rt[id].thr["cpu"]; got != model.StateCrit {
		t.Fatalf("restored threshold state %q", got)
	}
}

func TestRulePrecedence(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "fs01", func(d *model.Device) {
		d.Rules = []model.CheckRule{
			{Match: "disk:*", Mode: model.ModeOff},
			{Match: "disk:/var*", Mode: model.ModeQuiet}, // longer pattern wins
			{Match: "disk:/", Mode: model.ModeAlert},     // exact wins over every pattern
			{Match: "*", Mode: model.ModeQuiet},          // everything else quiet, but never reach
		}
	})
	e.report(t, id, diskAt("/", 96), diskAt("/var/log", 96), diskAt("/srv", 96), cpuAt(10))
	e.wait()
	st := e.c.Status(id)
	root, _ := findCheck(st, "disk:/")
	vlog, _ := findCheck(st, "disk:/var/log")
	cpu, _ := findCheck(st, "cpu")
	reach, _ := findCheck(st, "reach")
	if root.State != model.StateCrit || root.Mode != "" {
		t.Fatalf("exact: %+v", root)
	}
	if vlog.Mode != model.ModeQuiet || vlog.Muted != model.StateCrit {
		t.Fatalf("longest pattern: %+v", vlog)
	}
	if cpu.Mode != model.ModeQuiet || reach.Mode != "" || reach.State != model.StateOK {
		t.Fatalf("catch-all: cpu %+v reach %+v", cpu, reach)
	}
	if _, ok := findCheck(st, "disk:/srv"); ok || len(st.Hidden) != 1 || st.Hidden[0].Key != "disk:/srv" {
		t.Fatalf("hidden: %+v", st.Hidden)
	}
	if st.State != model.StateCrit || !slices.Equal(activeKeys(e), []string{"disk:/"}) {
		t.Fatalf("device %s alerts %v", st.State, activeKeys(e))
	}
}

func TestRuleReachUnaffected(t *testing.T) {
	e := newEnv(t, Options{})
	sw := fakesnmp.NewSwitch(8)
	e.c.SNMP.Dial = fakesnmp.Dialer(map[string]*fakesnmp.Device{"10.0.0.2": sw})
	d, fields, err := e.c.SaveDevice(model.Device{Name: "Switch", Type: model.TypeSNMP, Host: "10.0.0.2", Enabled: true,
		SNMP:  &model.SNMPConfig{Community: "public"},
		Rules: []model.CheckRule{{Match: "*", Mode: model.ModeOff}}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	ctx := context.Background()
	st, _ := e.c.PollNow(ctx, d.ID)
	if len(st.Checks) != 1 || st.Checks[0].Key != "reach" || len(st.Hidden) < 3 {
		t.Fatalf("all off: %+v hidden %d", st.Checks, len(st.Hidden))
	}
	if list, _ := e.st.SeriesList(d.ID); len(list) != 0 {
		t.Fatalf("metrics of off checks stored: %+v", list)
	}
	for _, h := range st.Hidden {
		if h.Key == "" || h.Name == "" {
			t.Fatalf("hidden entry: %+v", h)
		}
	}
	nHidden := len(st.Hidden)
	sw.SetReachable(false)
	e.c.PollNow(ctx, d.ID)
	st, _ = e.c.PollNow(ctx, d.ID)
	e.wait()
	if reach, _ := findCheck(st, "reach"); reach.State != model.StateCrit || st.State != model.StateCrit || len(st.Hidden) != nHidden {
		t.Fatalf("unreachable: %+v hidden %d", st, len(st.Hidden))
	}
	if !slices.Equal(activeKeys(e), []string{"reach"}) {
		t.Fatalf("alerts: %v", activeKeys(e))
	}

	// Ports quiet: shown, metrics stored, never alerting.
	sw.SetReachable(true)
	d.Rules = []model.CheckRule{{Match: "snmp:if:*", Mode: model.ModeQuiet}}
	if _, fields, err = e.c.SaveDevice(d); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	st, _ = e.c.PollNow(ctx, d.ID)
	ports := 0
	for _, cs := range st.Checks {
		if strings.HasPrefix(cs.Key, "snmp:if:") {
			ports++
			if cs.Mode != model.ModeQuiet || cs.State == model.StateWarn || cs.State == model.StateCrit {
				t.Fatalf("port: %+v", cs)
			}
		}
	}
	if ports == 0 || len(st.Hidden) != 0 {
		t.Fatalf("ports %d hidden %+v", ports, st.Hidden)
	}
}

func TestRuleValidation(t *testing.T) {
	e := newEnv(t, Options{})
	long := "disk:" + strings.Repeat("x", 200)
	_, fields, err := e.c.SaveDevice(model.Device{Name: "a", Type: model.TypeNet, Host: "h", Rules: []model.CheckRule{
		{Match: "  ", Mode: model.ModeOff},
		{Match: long, Mode: model.ModeOff},
		{Match: "cpu", Mode: "laut"},
		{Match: "cpu", Mode: model.ModeQuiet},
		{Match: "reach", Mode: model.ModeOff},
		{Match: "rea*", Mode: model.ModeQuiet},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"rules.0.match": "Check oder Muster angeben.",
		"rules.1.match": "Höchstens 200 Zeichen.",
		"rules.2.mode":  "Überwachen, Nur anzeigen oder Ausblenden auswählen.",
		"rules.3.match": "Doppelt (wie Zeile 3).",
		"rules.4.match": "Erreichbarkeit lässt sich nicht abschalten (dafür „Keine Alarme“ am Gerät).",
		"rules.5.match": "Erreichbarkeit lässt sich nicht abschalten (dafür „Keine Alarme“ am Gerät).",
	}
	if len(fields) != len(want) {
		t.Fatalf("errors: %v", fields)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s: %q, want %q", k, fields[k], v)
		}
	}

	// Valid rules are trimmed, an empty mode means alert, nil becomes [].
	d, fields, err := e.c.SaveDevice(model.Device{Name: "a", Type: model.TypeNet, Host: "h", Rules: []model.CheckRule{
		{Match: " net:* "}, {Match: "*", Mode: model.ModeOff}, {Match: "net:abc", Mode: model.ModeAlert}}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	if d.Rules[0].Match != "net:*" || d.Rules[0].Mode != model.ModeAlert || len(d.Rules) != 3 {
		t.Fatalf("rules: %+v", d.Rules)
	}
	d.Rules = nil
	if d, _, _ = e.c.SaveDevice(d); d.Rules == nil {
		t.Fatal("nil rules not normalized")
	}
}
