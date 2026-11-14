package pbs

import (
	"context"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakepbs"
	"pk95monitoring/internal/model"
)

func setup(t *testing.T) (*fakepbs.Server, *Collector, model.Device) {
	t.Helper()
	srv := fakepbs.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	return srv, New(), model.Device{ID: "p1", Name: "PBS", Type: model.TypePBS, Host: srv.Host(), PBS: &cfg}
}

func byKey(list []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range list {
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

func TestCollect(t *testing.T) {
	srv, c, d := setup(t)
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(checks)
	if s := m["pbs:server"]; s.State != model.StateOK || s.Facts["Version"] != "3.4.2" {
		t.Errorf("server %+v", s)
	}
	ds := m["pbs:ds:backup"]
	if u, ok := metric(ds, "storage.usage"); ds.State != model.StateOK || !ok || u < 68 || u > 69 || ds.Facts["Sicherungsgruppen"] != "5" || ds.Facts["Sicherungen"] != "56" {
		t.Errorf("datastore backup %+v", ds)
	}
	if off := m["pbs:ds:offsite"]; off.State != model.StateWarn || !strings.Contains(off.Message, "voll voraussichtlich in 1") {
		t.Errorf("datastore offsite %+v", off)
	}
	if v := m["pbs:verify:backup"]; v.State != model.StateCrit || !strings.Contains(v.Message, "vm/101") || v.Facts["Nicht verifiziert"] != "1" {
		t.Errorf("verify backup %+v", v)
	}
	if v := m["pbs:verify:offsite"]; v.State != model.StateOK || !strings.Contains(v.Message, "1 noch ausstehend") {
		t.Errorf("verify offsite %+v", v)
	}
	vm := m["pbs:group:backup:/vm/100"]
	if a, ok := metric(vm, "group.age"); vm.Name != "vm/100 dc01" || !ok || a < 2.9 || a > 4.1 || vm.Facts["Sicherungen"] != "14" || vm.Facts["Ziel"] != "backup" {
		t.Errorf("group vm/100 %+v", vm)
	}
	if _, ok := metric(vm, "backup.age"); ok {
		t.Error("VM groups must not carry backup.age (Proxmox VE judges guests)")
	}
	host := m["pbs:group:offsite:/host/laptop-gf"]
	if a, ok := metric(host, "backup.age"); !ok || a < 216 {
		t.Errorf("host group %+v", host)
	}
	if ns := m["pbs:group:backup:kunde-a/vm/100"]; ns.Name != "kunde-a: vm/100 app01" || ns.Facts["Namespace"] != "kunde-a" {
		t.Errorf("namespace group %+v", ns)
	}
	if s := m["pbs:task:syncjob:backup:offsite:s-nightly"]; s.State != model.StateCrit || !strings.Contains(s.Message, "Connection timed out") {
		t.Errorf("sync %+v", s)
	}
	if p := m["pbs:task:prunejob:backup:p-daily"]; p.State != model.StateWarn {
		t.Errorf("prune %+v", p)
	}
	if g := m["pbs:task:garbage_collection:offsite"]; g.State != model.StateOK || !strings.HasPrefix(g.Message, "Läuft gerade") {
		t.Errorf("gc %+v", g)
	}
	if _, ok := m["pbs:task:backup:backup:vm/100"]; ok {
		t.Error("backup tasks are judged per group, not as tasks")
	}
	for _, ch := range checks {
		if !model.BackupCheck(ch.Key) {
			t.Errorf("%s is not a backup check", ch.Key)
		}
	}

	// a second poll sees a changed task; snapshots come from the cache
	srv.SetTaskStatus("syncjob", "backup:offsite:s-nightly", "OK")
	checks, err = c.Collect(context.Background(), d)
	if err != nil || byKey(checks)["pbs:task:syncjob:backup:offsite:s-nightly"].State != model.StateOK {
		t.Fatalf("second poll: %v", err)
	}
}

func TestDatastoreFull(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.Local)
	u := dsUsage{Store: "s", Total: 100, Used: 50, Avail: 50, FullDate: num(now.Add(-time.Hour).Unix())}
	if ch := datastoreCheck(u, nil, now); ch.State != model.StateWarn || !strings.Contains(ch.Message, "in 0 Tagen") {
		t.Errorf("past estimate %+v", ch)
	}
	if ch := datastoreCheck(dsUsage{Store: "s", Error: "not mounted"}, nil, now); ch.State != model.StateUnknown {
		t.Errorf("error %+v", ch)
	}
}

func TestErrors(t *testing.T) {
	srv, c, d := setup(t)
	bad := *d.PBS
	bad.Secret = "falsch"
	d.PBS = &bad
	if _, err := c.Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "Token abgelehnt") {
		t.Errorf("wrong secret: %v", err)
	}
	wrongFP := srv.Config()
	wrongFP.Fingerprint = strings.Repeat("ab", 32)
	if _, err := Test(context.Background(), srv.Host(), wrongFP); err == nil || !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Errorf("fingerprint: %v", err)
	}
	res, err := Test(context.Background(), srv.Host(), srv.Config())
	if err != nil || res.Version != "3.4.2" || strings.Join(res.Datastores, ",") != "backup,offsite" {
		t.Errorf("test %+v %v", res, err)
	}
}
