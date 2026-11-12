package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// discoveryAgent is an agent without AutoAccept (the test default accepts everything).
func (e *env) discoveryAgent(t *testing.T, id string) string {
	t.Helper()
	return e.agentDevice(t, id, func(d *model.Device) { d.AutoAccept = false })
}

func mustCheck(t *testing.T, st model.DeviceStatus, key string) model.CheckStatus {
	t.Helper()
	cs, ok := findCheck(st, key)
	if !ok {
		t.Fatalf("check %s missing in %v", key, keysOf(st.Checks))
	}
	return cs
}

func keysOf(list []model.CheckStatus) []string {
	var out []string
	for _, cs := range list {
		out = append(out, cs.Key)
	}
	return out
}

func TestDiscoveryNewChecks(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.discoveryAgent(t, "db01")
	cpu := metricCheck("cpu", "CPU", model.Metric{Name: "cpu.usage", Value: 20, Unit: "%"})

	// The first report is the inventory: everything is monitored.
	e.report(t, id, diskCheck(40), cpu)
	st := e.c.Status(id)
	if dc := mustCheck(t, st, model.DiscoveryKey); dc.State != model.StateOK || dc.Message != "2 Dienste bekannt, nichts zu entscheiden" || dc.Group != "Status" {
		t.Fatalf("discovery after first report: %+v", dc)
	}
	if st.Inventory != nil {
		t.Fatal("Status exposes the inventory")
	}

	// A check found later is new: shown, never alerting, the discovery check warns.
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), cpu, diskAt("/data", 99))
	e.wait()
	st = e.c.Status(id)
	nc := mustCheck(t, st, "disk:/data")
	if nc.Mode != model.ModeNew || nc.State != model.StateOK || nc.Muted != model.StateCrit {
		t.Fatalf("new check: %+v", nc)
	}
	dc := mustCheck(t, st, model.DiscoveryKey)
	if dc.State != model.StateWarn || dc.Message != "1 neuer Dienst: Festplatte /data" {
		t.Fatalf("discovery: %+v", dc)
	}
	if st.State != model.StateWarn || !strings.Contains(st.Message, "Dienste-Inventur") {
		t.Fatalf("device: %s %q", st.State, st.Message)
	}
	if got := activeKeys(e); !slices.Equal(got, []string{model.DiscoveryKey}) {
		t.Fatalf("alerts: %v", got)
	}

	// Accept: the check is monitored (crit, alert), the discovery check is ok again at once.
	if acc, rm, err := e.c.Discovery(id, []string{"disk:/data", "nope"}, nil); err != nil || acc != 1 || rm != 0 {
		t.Fatalf("accept: %d %d %v", acc, rm, err)
	}
	e.wait()
	st = e.c.Status(id)
	if nc := mustCheck(t, st, "disk:/data"); nc.Mode != "" || nc.State != model.StateCrit {
		t.Fatalf("accepted: %+v", nc)
	}
	if dc := mustCheck(t, st, model.DiscoveryKey); dc.State != model.StateOK || dc.Message != "3 Dienste bekannt, nichts zu entscheiden" {
		t.Fatalf("discovery after accept: %+v", dc)
	}
	if got := activeKeys(e); !slices.Equal(got, []string{"disk:/data"}) {
		t.Fatalf("alerts after accept: %v", got)
	}
	// Accepting again changes nothing.
	if acc, _, _ := e.c.Discovery(id, []string{"disk:/data"}, nil); acc != 0 {
		t.Fatalf("accepted twice: %d", acc)
	}
}

func TestDiscoveryVanished(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.discoveryAgent(t, "web01")
	e.report(t, id, diskCheck(40), diskAt("/data", 10))

	// One report without /data is not enough (a single incomplete collection).
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40))
	if st := e.c.Status(id); len(st.Vanished) != 0 || mustCheck(t, st, model.DiscoveryKey).State != model.StateOK {
		t.Fatalf("vanished after one report: %+v", st.Vanished)
	}
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40))
	st := e.c.Status(id)
	if len(st.Vanished) != 1 || st.Vanished[0].Key != "disk:/data" || st.Vanished[0].Name != "Festplatte /data" {
		t.Fatalf("vanished: %+v", st.Vanished)
	}
	if dc := mustCheck(t, st, model.DiscoveryKey); dc.State != model.StateWarn || dc.Message != "1 Dienst verschwunden: Festplatte /data" {
		t.Fatalf("discovery: %+v", dc)
	}

	// Coming back clears it.
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/data", 10))
	if st := e.c.Status(id); len(st.Vanished) != 0 || mustCheck(t, st, model.DiscoveryKey).State != model.StateOK {
		t.Fatalf("back: %+v", st.Vanished)
	}

	// Removed from the inventory, it counts as new when it returns.
	for range 2 {
		e.clk.Add(time.Minute)
		e.report(t, id, diskCheck(40))
	}
	if _, rm, err := e.c.Discovery(id, nil, []string{"disk:/data", "disk:/"}); err != nil || rm != 1 {
		t.Fatalf("remove: %d %v", rm, err) // disk:/ is present: not removed
	}
	if st := e.c.Status(id); len(st.Vanished) != 0 || mustCheck(t, st, model.DiscoveryKey).State != model.StateOK {
		t.Fatalf("after remove: %+v", st.Vanished)
	}
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/data", 10))
	if cs := mustCheck(t, e.c.Status(id), "disk:/data"); cs.Mode != model.ModeNew {
		t.Fatalf("returned after remove: %+v", cs)
	}
}

func TestDiscoveryRulesAndAutoAccept(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.discoveryAgent(t, "fs01")
	e.report(t, id, diskCheck(40))

	// A matching rule decides a check found later: no new mark.
	e.setRules(t, id, model.CheckRule{Match: "disk:/mnt*", Mode: model.ModeQuiet})
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/mnt/a", 10), diskAt("/data", 10))
	st := e.c.Status(id)
	if cs := mustCheck(t, st, "disk:/mnt/a"); cs.Mode != model.ModeQuiet {
		t.Fatalf("ruled: %+v", cs)
	}
	if cs := mustCheck(t, st, "disk:/data"); cs.Mode != model.ModeNew {
		t.Fatalf("unruled: %+v", cs)
	}
	// A rule added afterwards decides a new check at once (and keeps deciding it).
	e.setRules(t, id, model.CheckRule{Match: "disk:/mnt*", Mode: model.ModeQuiet}, model.CheckRule{Match: "disk:/data", Mode: model.ModeQuiet})
	if cs := mustCheck(t, e.c.Status(id), "disk:/data"); cs.Mode != model.ModeQuiet {
		t.Fatalf("ruled later: %+v", cs)
	}
	e.setRules(t, id)
	if cs := mustCheck(t, e.c.Status(id), "disk:/data"); cs.Mode != "" {
		t.Fatalf("rule removed: %+v", cs)
	}

	// A new check that disappears again before a decision is forgotten, not vanished.
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/mnt/a", 10), diskAt("/data", 10), diskAt("/tmp", 5))
	for range 2 {
		e.clk.Add(time.Minute)
		e.report(t, id, diskCheck(40), diskAt("/mnt/a", 10), diskAt("/data", 10))
	}
	if st := e.c.Status(id); len(st.Vanished) != 0 || mustCheck(t, st, model.DiscoveryKey).State != model.StateOK {
		t.Fatalf("flicker: %+v %+v", st.Vanished, mustCheck(t, st, model.DiscoveryKey))
	}

	// AutoAccept monitors new checks at once.
	d, _ := e.c.Device(id)
	d.AutoAccept = true
	if _, errs, err := e.c.SaveDevice(d); err != nil || len(errs) > 0 {
		t.Fatal(err, errs)
	}
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/mnt/a", 10), diskAt("/data", 10), diskAt("/srv", 10))
	if cs := mustCheck(t, e.c.Status(id), "disk:/srv"); cs.Mode != "" {
		t.Fatalf("auto accepted: %+v", cs)
	}
}

func TestDiscoveryPersisted(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.discoveryAgent(t, "db01")
	e.report(t, id, diskCheck(40))
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40), diskAt("/data", 10))
	e.c.persist(true)
	c2, err := New(e.st, e.c.Opt)
	if err != nil {
		t.Fatal(err)
	}
	c2.Now = e.clk.Now
	if !c2.rt[id].newKeys["disk:/data"] || len(c2.rt[id].st.Inventory) != 2 {
		t.Fatalf("restored inventory: %+v", c2.rt[id].st.Inventory)
	}
	if cs := mustCheck(t, c2.Status(id), "disk:/data"); cs.Mode != model.ModeNew {
		t.Fatalf("restored mode: %+v", cs)
	}
}

func TestDiscoveryUnknownDevice(t *testing.T) {
	e := newEnv(t, Options{})
	if _, _, err := e.c.Discovery("nope", []string{"x"}, nil); err == nil {
		t.Fatal("unknown device accepted")
	}
}
