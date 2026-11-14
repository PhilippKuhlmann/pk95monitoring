package hyperv

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// sample is shaped like the output of Script on a host with five VMs.
const sample = `WARNING: noise before the JSON
{"available":true,"host":"HV01","vms":[
 {"id":"a1","name":"dc01","state":"Running","status":"Wird normal ausgeführt","heartbeat":"OkApplicationsHealthy","auto_start":"StartIfRunning",
  "cpu":7,"procs":4,"mem":8589934592,"demand":5368709120,"startup":8589934592,"dynamic":false,"uptime":86400,"generation":2,"version":"10.0",
  "clustered":false,"checkpoints":2,"oldest_checkpoint":%OLD%,"replication":{"state":"Replicating","health":"Warning","mode":"Primary","primary":"HV01","replica":"HV02","last":%LAST%}},
 {"id":"b2","name":"sql01","state":"Off","status":"","heartbeat":"","auto_start":"Start","cpu":0,"procs":8,"mem":0,"demand":0,"startup":17179869184,
  "dynamic":true,"uptime":0,"generation":2,"version":"10.0","clustered":false,"checkpoints":0,"oldest_checkpoint":0,"replication":null},
 {"id":"c3","name":"test","state":"Off","auto_start":"Nothing","checkpoints":0,"replication":null},
 {"id":"d4","name":"app01-replica","state":"Off","auto_start":"StartIfRunning","checkpoints":0,
  "replication":{"state":"Replicating","health":"Normal","mode":"Replica","primary":"HV03","replica":"HV01","last":%LAST%}},
 {"id":"e5","name":"full","state":"PausedCritical","status":"Angehalten – kritisch","auto_start":"Start","checkpoints":0}
]}`

func byKey(cs []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range cs {
		m[c.Key] = c
	}
	return m
}

func metric(c model.Check, name string) (float64, bool) {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}

func TestChecks(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	out := strings.NewReplacer("%OLD%", strconv.FormatInt(now.Add(-240*time.Hour).Unix(), 10),
		"%LAST%", strconv.FormatInt(now.Add(-15*time.Minute).Unix(), 10)).Replace(sample)
	r, err := Parse(out)
	if err != nil || !r.Available || r.Host != "HV01" || len(r.VMs) != 5 {
		t.Fatalf("parse %+v %v", r, err)
	}
	cs := Checks(r, nil, now)
	if cs[0].Key != KeyHost || cs[0].Message != "1 von 5 VMs laufen" {
		t.Errorf("host %+v", cs[0])
	}
	m := byKey(cs)
	dc := m["hv:vm:a1"]
	if dc.State != model.StateOK || dc.Name != "VM dc01" || !strings.HasPrefix(dc.Message, "läuft seit 1 Tag · CPU 7 %") || dc.Facts["Autostart"] != "wenn sie lief" {
		t.Errorf("dc01 %+v", dc)
	}
	if v, ok := metric(dc, "guest.mem"); !ok || v != 62.5 {
		t.Errorf("mem %v", dc.Metrics)
	}
	if v, ok := metric(dc, "checkpoint.age"); !ok || v != 10 || !strings.Contains(dc.Message, "2 Prüfpunkte (ältester vor 10 Tagen)") {
		t.Errorf("checkpoints %+v", dc)
	}
	if sql := m["hv:vm:b2"]; sql.State != model.StateCrit || !strings.HasPrefix(sql.Message, "VM läuft nicht") || sql.Facts["RAM"] != "16 GiB (dynamisch)" {
		t.Errorf("sql01 %+v", sql)
	}
	if tst := m["hv:vm:c3"]; tst.State != model.StateOK || tst.Message != "aus" {
		t.Errorf("test %+v", tst)
	}
	if rep := m["hv:vm:d4"]; rep.State != model.StateOK || rep.Message != "Replikat – aus" {
		t.Errorf("replica VM %+v", rep)
	}
	if full := m["hv:vm:e5"]; full.State != model.StateCrit || !strings.Contains(full.Message, "voll") || full.Facts["Status"] == "" {
		t.Errorf("paused %+v", full)
	}
	rp := m["hv:repl:a1"]
	if rp.State != model.StateWarn || rp.Message != "Replikation mit Warnung (Status Replicating) · HV02 · zuletzt vor 15 Min." || rp.Facts["Modus"] != "Primär" {
		t.Errorf("repl %+v", rp)
	}
	if rr := m["hv:repl:d4"]; rr.State != model.StateOK || !strings.Contains(rr.Message, "HV03") {
		t.Errorf("replica repl %+v", rr)
	}
	if _, ok := m["hv:repl:b2"]; ok {
		t.Error("repl check without replication")
	}
}

func TestHeartbeatAndSuspended(t *testing.T) {
	now := time.Now()
	r := Report{Available: true, VMs: []VM{
		{ID: "x", Name: "x", State: "Running", Heartbeat: "LostCommunication", Mem: 1 << 30},
		{ID: "y", Name: "y", State: "Running", Heartbeat: "NoContact", Replication: &Replication{State: "Suspended", Health: "Warning", Mode: "Primary"}},
		{ID: "z", Name: "z", State: "Running", Replication: &Replication{State: "Error", Health: "Critical", Mode: "Primary"}},
	}}
	m := byKey(Checks(r, nil, now))
	if x := m["hv:vm:x"]; x.State != model.StateWarn || !strings.HasPrefix(x.Message, "Heartbeat verloren") {
		t.Errorf("heartbeat %+v", x)
	}
	if y := m["hv:vm:y"]; y.State != model.StateOK {
		t.Errorf("no integration services is fine: %+v", y)
	}
	if y := m["hv:repl:y"]; y.State != model.StateWarn || !strings.HasPrefix(y.Message, "Replikation angehalten") {
		t.Errorf("suspended %+v", y)
	}
	if z := m["hv:repl:z"]; z.State != model.StateCrit {
		t.Errorf("error %+v", z)
	}
}

func TestParseVariants(t *testing.T) {
	if r, err := Parse(`{"available":false}`); err != nil || r.Available || Checks(r, nil, time.Now()) != nil {
		t.Errorf("no hyper-v: %+v %v", r, err)
	}
	r, err := Parse(`{"available":true,"host":"H","vms":{"id":"a","name":"one","state":"Running"}}`)
	if err != nil || len(r.VMs) != 1 || r.VMs[0].Name != "one" {
		t.Errorf("single VM: %+v %v", r, err)
	}
	if r, err := Parse(`{"available":true,"host":"H","vms":null}`); err != nil || len(Checks(r, nil, time.Now())) != 1 {
		t.Errorf("no VMs: %+v %v", r, err)
	}
	if _, err := Parse(""); err == nil {
		t.Error("empty output")
	}
	cs := Checks(Report{}, errors.New("Zugriff verweigert"), time.Now())
	if len(cs) != 1 || cs[0].State != model.StateUnknown || !strings.Contains(cs[0].Message, "Zugriff verweigert") {
		t.Errorf("error %+v", cs)
	}
}
