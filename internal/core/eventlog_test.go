package core

import (
	"context"
	"slices"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/winevent"
)

func ruleIDs(list []model.EventRule) []string {
	var out []string
	for _, r := range list {
		out = append(out, r.ID)
	}
	return out
}

func TestEventRulesReply(t *testing.T) {
	e := newEnv(t, Options{})
	lin := e.agentDevice(t, "lin", nil)
	win := e.agentDevice(t, "win", nil)
	if r := e.report(t, lin); len(r.EventRules) != 0 || r.EventRules == nil || r.EventIgnore == nil {
		t.Fatalf("linux agent got event rules: %+v", r.EventRules)
	}
	r := e.winReport(t, win)
	ids := ruleIDs(r.EventRules)
	if !slices.Contains(ids, "disk") || slices.Contains(ids, "system_errors") || len(r.EventIgnore) != 0 {
		t.Fatalf("default rules: %v", ids)
	}

	// central: switch the catch-all rule on, change a built-in one, add an own rule
	sysErr, _ := winevent.Default("system_errors")
	sysErr.Enabled = true
	if _, errs, err := e.c.SaveEventRule(sysErr); err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	logons, _ := winevent.Default("failed_logons")
	logons.Count = 50
	v, errs, err := e.c.SaveEventRule(logons)
	if err != nil || len(errs) > 0 || !v.Builtin || !v.Changed || v.Trigger != "ab 50 Ereignissen in der letzten Stunde: Warnung" {
		t.Fatalf("change built-in: %+v %v %v", v, errs, err)
	}
	own, errs, err := e.c.SaveEventRule(model.EventRule{Name: "SQL-Fehler", Enabled: true, Logs: []string{"Application"}, Sources: []string{"MSSQL*"}, Levels: []int{2}})
	if err != nil || len(errs) > 0 || own.Builtin || own.ID == "" {
		t.Fatalf("own rule: %+v %v %v", own, errs, err)
	}
	if _, errs, _ := e.c.SaveEventRule(model.EventRule{Name: "leer", Logs: []string{"System"}}); errs["ids"] == "" {
		t.Fatalf("rule without filter: %v", errs)
	}
	if _, _, err := e.c.SaveEventRule(model.EventRule{ID: "nope", Name: "x", Logs: []string{"System"}, IDs: []int{1}}); err == nil {
		t.Fatal("unknown rule saved")
	}

	// device: switch disk off, own rule, own exception; a central exception
	d, _ := e.c.Device(win)
	d.Agent.EventOff = []string{"disk", "Bad ID!"}
	d.Agent.EventRules = []model.EventRule{{Name: "Druckerfehler", Enabled: true, Logs: []string{"System"}, Sources: []string{"PrintService"}}}
	saved, errs, err := e.c.SaveDevice(d)
	if err != nil || len(errs) > 0 || !slices.Equal(saved.Agent.EventOff, []string{"disk"}) || saved.Agent.EventRules[0].ID == "" || saved.Agent.EventRules[0].Count != 1 {
		t.Fatalf("save device: %+v %v %v", saved.Agent, errs, err)
	}
	bad := saved.Clone()
	bad.Agent.EventRules = []model.EventRule{{Name: "", Logs: []string{"System"}, IDs: []int{1}}}
	if _, errs, _ := e.c.SaveDevice(bad); errs["agent.event_rules.0.name"] == "" {
		t.Fatalf("device rule error: %v", errs)
	}
	if _, errs, err := e.c.AddEventIgnore(win, model.EventIgnore{Source: "Service Control Manager", IDs: []int{7031}, Text: "Windows Search"}); err != nil || len(errs) > 0 {
		t.Fatalf("device exception: %v %v", errs, err)
	}
	central, errs, err := e.c.AddEventIgnore("", model.EventIgnore{Source: "Microsoft-Windows-DistributedCOM"})
	if err != nil || len(errs) > 0 {
		t.Fatalf("central exception: %v %v", errs, err)
	}
	if _, errs, _ := e.c.AddEventIgnore("", model.EventIgnore{Note: "nur Notiz"}); errs["source"] == "" {
		t.Fatal("empty exception accepted")
	}

	r = e.winReport(t, win)
	ids = ruleIDs(r.EventRules)
	if slices.Contains(ids, "disk") || !slices.Contains(ids, "system_errors") || !slices.Contains(ids, own.ID) || !slices.Contains(ids, saved.Agent.EventRules[0].ID) {
		t.Fatalf("effective rules: %v", ids)
	}
	for _, rule := range r.EventRules {
		if rule.ID == "failed_logons" && rule.Count != 50 {
			t.Fatalf("changed built-in not sent: %+v", rule)
		}
	}
	if len(r.EventIgnore) != 2 || r.EventIgnore[0].ID != central.ID {
		t.Fatalf("exceptions: %+v", r.EventIgnore)
	}

	// the device edit keeps the exception added meanwhile (nil = unchanged)
	d, _ = e.c.Device(win)
	d.Agent.EventIgnore = nil
	if saved, _, _ := e.c.SaveDevice(d); len(saved.Agent.EventIgnore) != 1 {
		t.Fatalf("exception lost: %+v", saved.Agent.EventIgnore)
	}

	// reset and delete
	if err := e.c.DeleteEventRule("failed_logons"); err != nil {
		t.Fatal(err)
	}
	if err := e.c.DeleteEventRule(own.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.DeleteEventRule(own.ID); err == nil {
		t.Fatal("deleted twice")
	}
	if err := e.c.DeleteEventIgnore("", central.ID); err != nil {
		t.Fatal(err)
	}
	ov := e.c.EventLog()
	for _, v := range ov.Rules {
		if v.ID == "failed_logons" && (v.Changed || v.Count != 20) || v.ID == own.ID {
			t.Fatalf("after reset/delete: %+v", v)
		}
		if v.ID == "disk" && v.Off != 1 {
			t.Fatalf("disk off count: %+v", v)
		}
	}
	if len(ov.Ignore) != 1 || ov.Ignore[0].DeviceID != win {
		t.Fatalf("exceptions: %+v", ov.Ignore)
	}
	// saving a built-in rule with its default settings drops the stored copy
	sysErr.Enabled = false
	if _, _, err := e.c.SaveEventRule(sysErr); err != nil || len(e.c.Settings().Agent.EventRules) != 0 {
		t.Fatalf("stored rules: %+v %v", e.c.Settings().Agent.EventRules, err)
	}
}

func TestEventLogOverview(t *testing.T) {
	e := newEnv(t, Options{})
	win := e.agentDevice(t, "win", func(d *model.Device) { d.Group = "Server" })
	now := e.clk.Now().Unix()
	ev := model.EventEntry{Time: now - 600, Log: "System", Source: "disk", ID: 7, Level: 2, Message: "Fehlerhafter Block"}
	e.winReport(t, win,
		model.Check{Key: "ev:disk", Name: "Festplattenfehler", Group: winevent.Group, State: model.StateCrit, Message: "1 Ereignis",
			Metrics: []model.Metric{{Name: winevent.MetricCount, Value: 1}}, Events: []model.EventEntry{ev}},
		model.Check{Key: "ev:system_errors", Name: "Alle Fehler (System)", Group: winevent.Group, State: model.StateWarn,
			Metrics: []model.Metric{{Name: winevent.MetricCount, Value: 1}}, Events: []model.EventEntry{ev}},
		model.Check{Key: "ev:lockout", Name: "Konto gesperrt", Group: winevent.Group, State: model.StateOK,
			Metrics: []model.Metric{{Name: winevent.MetricCount, Value: 0}}},
	)
	ov := e.c.EventLog()
	if ov.Agents != 1 || len(ov.Rows) != 3 || ov.Rows[0].CheckKey != "ev:disk" || ov.Rows[0].Group != "Server" || ov.Rows[0].Last != ev.Time {
		t.Fatalf("rows: %+v", ov.Rows)
	}
	if len(ov.Events) != 1 || ov.Events[0].DeviceID != win {
		t.Fatalf("events (one event, two rules): %+v", ov.Events)
	}
	if cs, ok := findCheck(e.c.Status(win), "ev:disk"); !ok || len(cs.Events) != 1 || cs.State != model.StateCrit {
		t.Fatalf("check status keeps the events: %+v", cs)
	}
}

// Event checks follow the configured rules: they are never "new" (undecided) like
// discovered services, and a removed rule is not reported as vanished.
func TestEventChecksNotDiscovered(t *testing.T) {
	e := newEnv(t, Options{})
	win := e.agentDevice(t, "win", func(d *model.Device) { d.AutoAccept = false })
	svc := model.Check{Key: "svc:Spooler", Name: "Druckwarteschlange", Group: "Dienste", State: model.StateOK}
	e.winReport(t, win, svc)
	ev := model.Check{Key: "ev:service_crash", Name: "Dienst abgestürzt", Group: winevent.Group, State: model.StateWarn, Message: "1 Ereignis"}
	e.winReport(t, win, svc, ev)
	st := e.c.Status(win)
	if cs, _ := findCheck(st, "ev:service_crash"); cs.State != model.StateWarn || cs.Mode != "" {
		t.Fatalf("event check: %+v", cs)
	}
	for _, it := range st.Inventory {
		if it.Key == ev.Key {
			t.Fatalf("event check in the inventory: %+v", st.Inventory)
		}
	}
	e.winReport(t, win, svc)
	e.winReport(t, win, svc)
	if dc, _ := findCheck(e.c.Status(win), model.DiscoveryKey); dc.State != model.StateOK {
		t.Fatalf("removed rule reported as vanished: %+v", dc)
	}
}

// Updates and event rules are separate: they alert, but neither colour the device nor count
// with its checks or in the availability report.
func TestSeparateChecks(t *testing.T) {
	e := newEnv(t, Options{})
	win := e.agentDevice(t, "win", nil)
	start := e.clk.Now().Unix()
	upd := model.Check{Key: "updates", Name: "Updates", Group: "Updates", State: model.StateWarn, Message: "Neustart erforderlich"}
	ev := model.Check{Key: "ev:disk", Name: "Festplattenfehler", Group: winevent.Group, State: model.StateCrit, Message: "1 Ereignis"}
	cpu := metricCheck("cpu", "CPU", model.Metric{Name: "cpu.usage", Value: 10, Unit: "%"})
	e.winReport(t, win, upd, ev, cpu)
	e.wait()
	st := e.c.Status(win)
	if st.State != model.StateOK || st.Message != "" {
		t.Fatalf("device: %s %q", st.State, st.Message)
	}
	if got := activeKeys(e); !slices.Contains(got, "updates") || !slices.Contains(got, "ev:disk") {
		t.Fatalf("alerts: %v", got)
	}
	if r, _ := rowOf(e.c.Rows(), win); r.Checks.Warn+r.Checks.Crit != 0 || r.Checks.OK != 3 { // cpu, reach, inventory
		t.Fatalf("counts: %+v", r.Checks)
	}
	e.clk.t = e.clk.t.Add(time.Hour)
	rep, err := e.c.Report(context.Background(), start, e.clk.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Devices {
		if d.ID == win && (d.Alerts != 0 || d.ProblemTime != 0) {
			t.Fatalf("report: %+v", d)
		}
	}
}
