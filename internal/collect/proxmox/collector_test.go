package proxmox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakepve"
	"pk95monitoring/internal/model"
)

// units are the metric units allowed by DESIGN.md for the PVE collector.
var units = map[string]string{
	"cpu.usage": "%", "cpu.load1": "", "mem.usage": "%", "mem.used": "B", "mem.total": "B",
	"swap.usage": "%", "disk.usage": "%", "uptime": "s",
	"updates.pending": "", "updates.security": "",
	"guest.cpu": "%", "guest.mem": "%", "guest.mem_used": "B",
	"guest.netin": "B/s", "guest.netout": "B/s", "guest.diskread": "B/s", "guest.diskwrite": "B/s",
	"storage.usage": "%", "storage.used": "B", "storage.total": "B",
}

func setup(t *testing.T) (*fakepve.Server, *Collector, model.Device) {
	t.Helper()
	srv := fakepve.New()
	t.Cleanup(srv.Close)
	c := New()
	c.now = srv.Now
	cfg := srv.Config()
	d := model.Device{ID: "pve", Name: "Cluster", Type: model.TypeProxmox, Host: srv.Host(), Enabled: true, Proxmox: &cfg}
	return srv, c, d
}

func collect(t *testing.T, c *Collector, d model.Device) map[string]model.Check {
	t.Helper()
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]model.Check{}
	for _, ch := range checks {
		if _, dup := out[ch.Key]; dup {
			t.Errorf("duplicate key %s", ch.Key)
		}
		out[ch.Key] = ch
	}
	return out
}

func metric(ch model.Check, name string) (model.Metric, bool) {
	for _, m := range ch.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return model.Metric{}, false
}

func want(t *testing.T, m map[string]model.Check, key, state, name, group string) model.Check {
	t.Helper()
	ch, ok := m[key]
	if !ok {
		t.Fatalf("check %s missing", key)
	}
	if ch.State != state {
		t.Errorf("%s: state %q, want %q (%s)", key, ch.State, state, ch.Message)
	}
	if name != "" && ch.Name != name {
		t.Errorf("%s: name %q, want %q", key, ch.Name, name)
	}
	if group != "" && ch.Group != group {
		t.Errorf("%s: group %q, want %q", key, ch.Group, group)
	}
	return ch
}

func TestCollectChecks(t *testing.T) {
	_, c, d := setup(t)
	m := collect(t, c, d)

	for key, ch := range m {
		if ch.Name == "" || ch.Group == "" || model.Rank(ch.State) == 1 && ch.State != model.StateUnknown {
			t.Errorf("%s: incomplete check %+v", key, ch)
		}
		for _, mt := range ch.Metrics {
			u, ok := units[mt.Name]
			if !ok || u != mt.Unit {
				t.Errorf("%s: metric %s unit %q, want %q (known %v)", key, mt.Name, mt.Unit, u, ok)
			}
		}
	}

	cl := want(t, m, "pve:cluster", model.StateOK, "Cluster", "Cluster")
	if cl.Facts["Cluster"] != "demo" || cl.Facts["PVE-Version"] != "8.3.2" {
		t.Errorf("cluster facts %v", cl.Facts)
	}
	for _, n := range []string{"pve1", "pve2", "pve3"} {
		ch := want(t, m, "pve:node:"+n, model.StateOK, "Knoten "+n, "Knoten")
		for _, name := range []string{"cpu.usage", "mem.usage", "mem.used", "mem.total", "disk.usage", "uptime", "swap.usage", "cpu.load1"} {
			mt, ok := metric(ch, name)
			if !ok || mt.Instance != n {
				t.Errorf("%s: metric %s missing or wrong instance %q", ch.Key, name, mt.Instance)
			}
		}
		if mt, _ := metric(ch, "cpu.usage"); mt.Value <= 0 || mt.Value >= 100 {
			t.Errorf("%s cpu %v", n, mt.Value)
		}
		for _, f := range []string{"CPU", "Kerne", "PVE-Version", "Kernel"} {
			if ch.Facts[f] == "" {
				t.Errorf("%s: fact %s missing (%v)", n, f, ch.Facts)
			}
		}
		if ch.Facts["Kernel"] != "6.8.12-5-pve" {
			t.Errorf("kernel fact %q", ch.Facts["Kernel"])
		}
	}

	vm := want(t, m, "pve:vm:100", model.StateOK, "VM 100 dc01", "VMs")
	for _, name := range []string{"guest.cpu", "guest.mem", "guest.mem_used"} {
		if mt, ok := metric(vm, name); !ok || mt.Instance != "vm/100" {
			t.Errorf("vm/100: %s missing or instance %q", name, mt.Instance)
		}
	}
	if _, ok := metric(vm, "guest.netin"); ok {
		t.Error("rate on first sample")
	}
	want(t, m, "pve:ct:200", model.StateOK, "CT 200 unifi", "Container")
	if mt, _ := metric(m["pve:ct:200"], "guest.cpu"); mt.Instance != "ct/200" {
		t.Errorf("ct instance %q", mt.Instance)
	}
	if _, ok := m["pve:vm:9000"]; ok {
		t.Error("template must be skipped")
	}
	guests := 0
	for k := range m {
		if strings.HasPrefix(k, "pve:vm:") || strings.HasPrefix(k, "pve:ct:") {
			guests++
		}
	}
	if guests != 12 {
		t.Errorf("guests %d, want 12", guests)
	}
	if ch := want(t, m, "pve:vm:107", model.StateCrit, "VM 107 erp", "VMs"); ch.Message != "VM läuft nicht" {
		t.Errorf("107 message %q", ch.Message)
	}
	if ch := want(t, m, "pve:vm:106", model.StateOK, "", ""); ch.Message != "gestoppt" {
		t.Errorf("106 message %q", ch.Message)
	}

	st := want(t, m, "pve:storage:pve1:local", model.StateOK, "Speicher local (pve1)", "Speicher")
	if mt, ok := metric(st, "storage.usage"); !ok || mt.Instance != "pve1/local" || mt.Value <= 0 {
		t.Errorf("storage metric %+v", mt)
	}
	want(t, m, "pve:storage:pve1:backup", model.StateOK, "", "Speicher")
	if _, ok := m["pve:storage:pve2:backup"]; ok {
		t.Error("shared storage reported twice")
	}
	want(t, m, "pve:storage:pve3:tank", model.StateOK, "", "")
	want(t, m, "pve:zfs:pve3:tank", model.StateOK, "ZFS tank (pve3)", "Speicher")
	want(t, m, "pve:zfs:pve1:rpool", model.StateOK, "", "")
	if ch := want(t, m, "pve:disk:pve1:sda", model.StateWarn, "Platte sda (pve1)", "Platten"); ch.Facts["Verschleiß"] != "92 %" {
		t.Errorf("wearout fact %v", ch.Facts)
	}
	want(t, m, "pve:disk:pve1:nvme0n1", model.StateOK, "", "")
	if ch := want(t, m, "pve:disk:pve3:sdb", model.StateOK, "", ""); ch.Facts["Verschleiß"] != "" {
		t.Errorf("HDD with wearout %v", ch.Facts)
	}

	up := want(t, m, "pve:updates:pve1", model.StateOK, "Updates pve1", "Updates")
	if p, _ := metric(up, "updates.pending"); p.Value != 6 || p.Instance != "pve1" {
		t.Errorf("pending %+v", p)
	}
	if s, _ := metric(up, "updates.security"); s.Value != 2 {
		t.Errorf("security %+v", s)
	}
	if p, _ := metric(m["pve:updates:pve3"], "updates.pending"); p.Value != 0 {
		t.Errorf("pve3 pending %v", p.Value)
	}

	bk := want(t, m, "pve:backup", model.StateCrit, "Sicherungen", "Sicherungen")
	if !strings.Contains(bk.Message, "103 nextcloud") || strings.Contains(bk.Message, "105") {
		t.Errorf("backup message %q", bk.Message)
	}
}

func TestRates(t *testing.T) {
	srv, c, d := setup(t)
	collect(t, c, d)
	srv.Advance(60 * time.Second)
	m := collect(t, c, d)
	vm := m["pve:vm:101"]
	for _, name := range rateNames {
		mt, ok := metric(vm, name)
		if !ok || mt.Unit != "B/s" || mt.Instance != "vm/101" || mt.Value <= 0 {
			t.Errorf("%s: %+v (present %v)", name, mt, ok)
		}
	}
	// fileserver averages 2.5 MB/s in; the rate varies between 50 % and 150 %.
	if in, _ := metric(vm, "guest.netin"); in.Value < 1e6 || in.Value > 4e6 {
		t.Errorf("netin rate %v", in.Value)
	}

	// Restarted guest: counters start from zero, so no rate on the next poll.
	srv.SetGuestStatus(101, "stopped")
	collect(t, c, d)
	srv.SetGuestStatus(101, "running")
	srv.Advance(60 * time.Second)
	m = collect(t, c, d)
	if _, ok := metric(m["pve:vm:101"], "guest.netin"); ok {
		t.Error("rate after restart")
	}
}

func TestRatesCounterDecrease(t *testing.T) {
	c := New()
	t0 := time.Unix(1_700_000_000, 0)
	g := resource{NetIn: 1000, NetOut: 1000, DiskRead: 1000, DiskWrite: 1000}
	if r := c.rates("d", "vm/1", g, t0); r != nil {
		t.Fatalf("first sample: %v", r)
	}
	g.NetIn, g.NetOut = 500, 3000 // netin wrapped/reset, netout grew by 2000
	r := c.rates("d", "vm/1", g, t0.Add(10*time.Second))
	if len(r) != 3 || r[0].Name != "guest.netout" || r[0].Value != 200 {
		t.Errorf("rates %+v", r)
	}
	if r := c.rates("d", "vm/1", g, t0.Add(10*time.Second)); r != nil {
		t.Errorf("zero interval: %v", r)
	}
}

func TestNodeOffline(t *testing.T) {
	srv, c, d := setup(t)
	srv.SetNodeOnline("pve3", false)
	m := collect(t, c, d)
	if ch := want(t, m, "pve:node:pve3", model.StateCrit, "", ""); ch.Message != "Knoten offline" || len(ch.Metrics) != 0 {
		t.Errorf("offline node %+v", ch)
	}
	if ch := want(t, m, "pve:cluster", model.StateWarn, "", ""); !strings.Contains(ch.Message, "2 von 3") {
		t.Errorf("cluster message %q", ch.Message)
	}
	want(t, m, "pve:vm:105", model.StateUnknown, "", "")
	want(t, m, "pve:vm:106", model.StateUnknown, "", "")
	want(t, m, "pve:ct:202", model.StateUnknown, "", "")
	want(t, m, "pve:storage:pve3:tank", model.StateUnknown, "", "")
	want(t, m, "pve:node:pve1", model.StateOK, "", "")
	for k := range m {
		if strings.HasPrefix(k, "pve:zfs:pve3:") || strings.HasPrefix(k, "pve:disk:pve3:") || k == "pve:updates:pve3" {
			t.Errorf("details of offline node: %s", k)
		}
	}

	srv.SetNodeOnline("pve2", false)
	m = collect(t, c, d)
	if ch := want(t, m, "pve:cluster", model.StateCrit, "", ""); !strings.Contains(ch.Message, "Kein Quorum") {
		t.Errorf("cluster message %q", ch.Message)
	}
	// The shared NFS storage moves to the only node left.
	want(t, m, "pve:storage:pve1:backup", model.StateOK, "", "")
}

func TestWatchGuests(t *testing.T) {
	tests := []struct {
		watch          string
		ignore         []string
		state106, s107 string
	}{
		{"", nil, model.StateOK, model.StateCrit},
		{"onboot", nil, model.StateOK, model.StateCrit},
		{"all", nil, model.StateCrit, model.StateCrit},
		{"all", []string{"106"}, model.StateOK, model.StateCrit},
		{"none", nil, model.StateOK, model.StateOK},
		{"onboot", []string{" 107 "}, model.StateOK, model.StateOK},
	}
	srv, _, base := setup(t)
	srv.SetGuestStatus(202, "stopped") // container with onboot
	for _, tt := range tests {
		t.Run(tt.watch+strings.Join(tt.ignore, ","), func(t *testing.T) {
			c := New()
			c.now = srv.Now
			cfg := *base.Proxmox
			cfg.WatchGuests, cfg.IgnoreGuests = tt.watch, tt.ignore
			d := base
			d.Proxmox = &cfg
			m := collect(t, c, d)
			want(t, m, "pve:vm:106", tt.state106, "", "")
			want(t, m, "pve:vm:107", tt.s107, "", "")
			if tt.watch != "none" {
				if ch := want(t, m, "pve:ct:202", model.StateCrit, "", ""); ch.Message != "Container läuft nicht" {
					t.Errorf("ct message %q", ch.Message)
				}
			}
		})
	}
}

func TestBackupOK(t *testing.T) {
	srv, c, d := setup(t)
	srv.SetBackupFailed(false)
	ch := want(t, collect(t, c, d), "pve:backup", model.StateOK, "", "")
	if ch.Facts["Letzte Sicherung"] == "" || !strings.HasPrefix(ch.Message, "Alle Sicherungen erfolgreich") {
		t.Errorf("backup %+v", ch)
	}
}

func TestStorageZFSStates(t *testing.T) {
	srv, c, d := setup(t)
	srv.SetStorageUsage("pve1", "local", 91.5)
	srv.SetZFSHealth("pve3", "tank", "DEGRADED")
	srv.SetZFSHealth("pve1", "rpool", "FAULTED")
	m := collect(t, c, d)
	if mt, _ := metric(m["pve:storage:pve1:local"], "storage.usage"); mt.Value != 91.5 {
		t.Errorf("usage %v", mt.Value)
	}
	if ch := want(t, m, "pve:zfs:pve3:tank", model.StateWarn, "", ""); !strings.HasPrefix(ch.Message, "DEGRADED") {
		t.Errorf("zfs message %q", ch.Message)
	}
	want(t, m, "pve:zfs:pve1:rpool", model.StateCrit, "", "")
}

func TestForbidden(t *testing.T) {
	srv, c, d := setup(t)
	srv.SetForbidden("/apt/update", true)
	srv.SetForbidden("/disks/list", true)
	srv.SetForbidden("/tasks", true)
	m := collect(t, c, d)
	if ch := want(t, m, "pve:backup", model.StateUnknown, "", ""); !strings.HasPrefix(ch.Message, "Keine Berechtigung (") {
		t.Errorf("backup message %q", ch.Message)
	}
	for k := range m {
		// PVEAuditor cannot list apt updates: the check is left out instead of making the
		// whole device unknown.
		if strings.HasPrefix(k, "pve:disk:") || strings.HasPrefix(k, "pve:updates:") {
			t.Errorf("check without permission: %s", k)
		}
	}
	ch := want(t, m, "pve:node:pve1", model.StateOK, "", "")
	if !strings.HasPrefix(ch.Facts["Hinweis Platten"], "Keine Berechtigung") || ch.Facts["Hinweis Updates"] != "Keine Berechtigung (Sys.Modify auf /nodes)" {
		t.Errorf("node facts %v", ch.Facts)
	}
	// The missing permission is cached for an hour; afterwards it is asked again.
	srv.SetForbidden("/apt/update", false)
	if _, ok := collect(t, c, d)["pve:updates:pve1"]; ok {
		t.Error("apt permission not cached")
	}
	srv.Advance(61 * time.Minute)
	m = collect(t, c, d)
	want(t, m, "pve:updates:pve1", model.StateOK, "", "")
	if h := m["pve:node:pve1"].Facts["Hinweis Updates"]; h != "" {
		t.Errorf("stale hint %q", h)
	}
	// Other apt failures stay visible as unknown.
	if ch := updatesCheck("pve1", aptEntry{err: &APIError{Status: 500, Message: "apt lock"}}); ch.State != model.StateUnknown || ch.Message != "Abfrage fehlgeschlagen: apt lock" {
		t.Errorf("apt error check %+v", ch)
	}
}

func TestBackupPartialNodes(t *testing.T) {
	srv, c, d := setup(t)
	srv.SetForbidden("/nodes/pve1/tasks", true)
	ch := want(t, collect(t, c, d), "pve:backup", model.StateCrit, "", "")
	if !strings.Contains(ch.Message, "103 nextcloud") || !strings.HasPrefix(ch.Facts["Hinweis"], "Aufgaben von pve1 nicht lesbar") {
		t.Errorf("backup %+v", ch)
	}
}

func TestBackupLogCache(t *testing.T) {
	srv, c, d := setup(t)
	want(t, collect(t, c, d), "pve:backup", model.StateCrit, "", "")
	// Task logs are not read again: the failed guest is still named without access to them.
	srv.SetForbidden("/log", true)
	if ch := want(t, collect(t, c, d), "pve:backup", model.StateCrit, "", ""); !strings.Contains(ch.Message, "103 nextcloud") {
		t.Errorf("backup message %q", ch.Message)
	}
	// Without the log a new outcome of the same task is reported by its job.
	srv.SetBackupFailed(false)
	want(t, collect(t, c, d), "pve:backup", model.StateOK, "", "")
	srv.SetBackupFailed(true)
	if ch := want(t, collect(t, c, d), "pve:backup", model.StateCrit, "", ""); !strings.Contains(ch.Message, "Job auf pve2 (job errors)") {
		t.Errorf("backup message %q", ch.Message)
	}
}

func TestRateMinInterval(t *testing.T) {
	srv, c, d := setup(t)
	collect(t, c, d)
	srv.Advance(3 * time.Second) // manual poll right after the scheduled one
	if _, ok := metric(collect(t, c, d)["pve:vm:101"], "guest.netin"); ok {
		t.Error("rate over 3 s")
	}
	srv.Advance(57 * time.Second)
	in, ok := metric(collect(t, c, d)["pve:vm:101"], "guest.netin")
	// The base is still the first poll (60 s ago), not the manual one.
	if !ok || in.Value < 1e6 || in.Value > 4e6 {
		t.Errorf("netin %+v (present %v)", in, ok)
	}
}

func TestPruneCaches(t *testing.T) {
	_, c, d := setup(t)
	c.mu.Lock()
	st := c.dev(d.ID)
	st.onboot["qemu/4711"] = onbootEntry{on: true, at: time.Now()}
	st.apt["gone"] = aptEntry{at: time.Now()}
	c.mu.Unlock()
	collect(t, c, d)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := st.onboot["qemu/4711"]; ok {
		t.Error("onboot of deleted guest kept")
	}
	if _, ok := st.apt["gone"]; ok {
		t.Error("apt of removed node kept")
	}
	if _, ok := st.onboot["qemu/107"]; !ok {
		t.Error("onboot of stopped guest not cached")
	}
	if len(st.taskLogs) == 0 {
		t.Error("task log not cached")
	}
}

func TestCollectErrors(t *testing.T) {
	srv, _, base := setup(t)
	tests := []struct {
		name string
		mod  func(*model.Device)
		want string
	}{
		{"wrong token", func(d *model.Device) { d.Proxmox.Secret = "falsch" }, "Token ungültig"},
		{"wrong fingerprint", func(d *model.Device) { d.Proxmox.Fingerprint = strings.Repeat("AB:", 31) + "AB" }, "Fingerabdruck stimmt nicht"},
		{"no fingerprint", func(d *model.Device) { d.Proxmox.Fingerprint = "" }, "nicht vertrauenswürdig"},
		{"unreachable", func(d *model.Device) { d.Proxmox.Port = 1 }, "Keine Verbindung"},
		{"no config", func(d *model.Device) { d.Proxmox = nil }, "Proxmox-Zugang fehlt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := base
			cfg := *base.Proxmox
			d.Proxmox = &cfg
			tt.mod(&d)
			_, err := New().Collect(context.Background(), d)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want %q", err, tt.want)
			}
		})
	}
	_ = srv
}

func TestProbeAndTest(t *testing.T) {
	srv, _, d := setup(t)
	fp, err := Probe(context.Background(), srv.Host(), srv.Port())
	if err != nil || fp != srv.Fingerprint() {
		t.Fatalf("Probe = %q, %v; want %q", fp, err, srv.Fingerprint())
	}
	res, err := Test(context.Background(), srv.Host(), *d.Proxmox)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if res.Version != "8.3.2" || strings.Join(res.Nodes, ",") != "pve1,pve2,pve3" || res.Guests != 12 {
		t.Errorf("Test = %+v", res)
	}
	bad := *d.Proxmox
	bad.Secret = "x"
	if _, err := Test(context.Background(), srv.Host(), bad); err == nil || !strings.Contains(err.Error(), "Token ungültig oder ohne Rechte") {
		t.Errorf("wrong token: %v", err)
	}
	bad = *d.Proxmox
	bad.TokenID = "root@pam"
	if _, err := Test(context.Background(), srv.Host(), bad); err == nil || !strings.Contains(err.Error(), "Token-ID") {
		t.Errorf("token id without !: %v", err)
	}
	bad = *d.Proxmox
	bad.Fingerprint = "00"
	if _, err := Test(context.Background(), srv.Host(), bad); err == nil || !strings.Contains(err.Error(), "Fingerabdruck stimmt nicht") {
		t.Errorf("wrong fingerprint: %v", err)
	}
}

func TestConcurrentDevices(t *testing.T) {
	srv, c, d := setup(t)
	var wg sync.WaitGroup
	for i := range 4 {
		dd := d
		dd.ID = "dev" + string(rune('a'+i))
		wg.Go(func() {
			for range 3 {
				if _, err := c.Collect(context.Background(), dd); err != nil {
					t.Error(err)
				}
				srv.Advance(time.Second)
			}
		})
	}
	wg.Wait()
	c.Forget("deva")
	if _, ok := c.devs["deva"]; ok {
		t.Error("Forget kept state")
	}
}
