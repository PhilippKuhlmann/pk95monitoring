package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/fake/fakepve"
	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

func TestPollFailure(t *testing.T) {
	e := newEnv(t, Options{})
	sw := fakesnmp.NewSwitch(8)
	e.c.SNMP.Dial = fakesnmp.Dialer(map[string]*fakesnmp.Device{"10.0.0.2": sw})
	d, fields, err := e.c.SaveDevice(model.Device{Name: "Switch", Type: model.TypeSNMP, Host: "10.0.0.2", Enabled: true,
		SNMP: &model.SNMPConfig{Community: "public"}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	ctx := context.Background()
	st, err := e.c.PollNow(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	reach, ok := findCheck(st, "reach")
	if !ok || reach.State != model.StateOK || reach.Name != "Erreichbarkeit" || reach.Group != "Status" || len(st.Checks) < 3 {
		t.Fatalf("first poll: %+v", st)
	}
	n := len(st.Checks)

	sw.SetReachable(false)
	st, _ = e.c.PollNow(ctx, d.ID)
	reach, _ = findCheck(st, "reach")
	if reach.State != model.StateWarn || st.State != model.StateWarn || st.Error == "" || len(st.Checks) != n {
		t.Fatalf("first failure: %+v", st)
	}
	for _, cs := range st.Checks {
		if cs.Key != "reach" && (cs.State != model.StateUnknown || cs.Message != "Keine aktuellen Daten") {
			t.Fatalf("stale check: %+v", cs)
		}
	}
	if !strings.Contains(st.Message, "SNMP") {
		t.Fatalf("message should carry the error: %q", st.Message)
	}
	st, _ = e.c.PollNow(ctx, d.ID)
	if reach, _ = findCheck(st, "reach"); reach.State != model.StateCrit || st.State != model.StateCrit {
		t.Fatalf("second failure: %+v", st)
	}
	sw.SetReachable(true)
	st, _ = e.c.PollNow(ctx, d.ID)
	if reach, _ = findCheck(st, "reach"); reach.State != model.StateOK || st.Error != "" {
		t.Fatalf("recovered: %+v", st)
	}
}

func writeAgentBinary(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, agentdist.FileName("linux", "amd64")), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRegisterReportUpdate(t *testing.T) {
	dir := t.TempDir()
	writeAgentBinary(t, dir)
	e := newEnv(t, Options{AgentDir: dir, Version: "1.2.0"})
	ctx := context.Background()
	reg := model.AgentRegister{Key: "falsch", AgentID: "m-1", Hostname: "web01", OS: "linux", Platform: "debian",
		PlatformVersion: "13", Arch: "amd64", Version: "1.1.0", IPs: []string{"127.0.0.1", "fe80::1", "10.0.0.21"}}
	if _, err := e.c.RegisterAgent(ctx, reg, "192.0.2.1"); !errors.Is(err, ErrBadEnrollKey) {
		t.Fatalf("wrong key: %v", err)
	}
	key := e.c.EnrollKey()
	if len(key) != 24 || key != e.c.EnrollKey() {
		t.Fatalf("enroll key %q", key)
	}
	reg.Key = strings.ToLower(key)
	r1, err := e.c.RegisterAgent(ctx, reg, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.c.Device(r1.DeviceID)
	if d.Type != model.TypeAgent || d.Name != "web01" || d.Host != "10.0.0.21" || !d.Enabled || len(r1.Token) != 64 || r1.Interval != 60 {
		t.Fatalf("registered: %+v %+v", d, r1)
	}
	// Re-registration finds the device by agent ID and replaces the token.
	r2, err := e.c.RegisterAgent(ctx, reg, "192.0.2.1")
	if err != nil || r2.DeviceID != r1.DeviceID || r2.Token == r1.Token || len(e.c.Devices()) != 1 {
		t.Fatalf("re-register: %v %+v", err, r2)
	}
	if _, err := e.c.AgentReport(ctx, r1.Token, model.AgentReport{}, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old token: %v", err)
	}
	rep := model.AgentReport{Hostname: "web01", OS: "linux", Platform: "debian", PlatformVersion: "13.1", Arch: "amd64",
		Version: "1.1.0", Time: e.clk.Now().Unix(), Checks: []model.Check{diskCheck(40)}}
	reply, err := e.c.AgentReport(ctx, r2.Token, rep, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Update == nil || reply.Update.Version != "1.2.0" || reply.Update.URL != "/agent/download/linux/amd64" || len(reply.Update.SHA256) != 64 {
		t.Fatalf("update offer: %+v", reply.Update)
	}
	d, _ = e.c.Device(r1.DeviceID)
	if d.Agent.PlatformVersion != "13.1" || d.Agent.LastSeen != e.clk.Now().Unix() {
		t.Fatalf("facts: %+v", d.Agent)
	}
	if st := e.c.Status(d.ID); st.State != model.StateOK || st.LastPoll == 0 {
		t.Fatalf("status: %+v", st)
	}
	rows := e.c.Rows()
	if len(rows) != 1 || rows[0].Platform != "Debian 13.1" || rows[0].Disk == nil || *rows[0].Disk != 40 {
		t.Fatalf("row: %+v", rows)
	}

	// No update: same version, other platform without binary, auto-update off.
	rep.Version = "1.2.0"
	if reply, _ = e.c.IngestReport(d.ID, rep); reply.Update != nil {
		t.Fatal("same version offered")
	}
	rep.Version, rep.OS = "1.0.0", "windows"
	if reply, _ = e.c.IngestReport(d.ID, rep); reply.Update != nil {
		t.Fatal("missing binary offered")
	}
	rep.OS = "linux"
	e.c.settings.Agent.AutoUpdate = false
	if reply, _ = e.c.IngestReport(d.ID, rep); reply.Update != nil {
		t.Fatal("auto-update off but offered")
	}
}

func TestAgentDevUpdate(t *testing.T) {
	dir := t.TempDir()
	writeAgentBinary(t, dir)
	e := newEnv(t, Options{AgentDir: dir, Version: "dev"})
	id := e.agentDevice(t, "a", nil)
	if r, _ := e.c.IngestReport(id, model.AgentReport{OS: "linux", Arch: "amd64", Version: "dev", Time: e.clk.Now().Unix()}); r.Update != nil {
		t.Fatal("dev to dev offered")
	}
}

func TestAgentSilenceAndBuffered(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "db01", nil)
	e.report(t, id, diskCheck(40))
	t0 := e.clk.Now().Unix()

	// A buffered report (older, bad) only adds history.
	e.clk.Add(time.Minute)
	if _, err := e.c.IngestReport(id, model.AgentReport{Buffered: true, Time: t0 - 600, Checks: []model.Check{diskCheck(99)}}); err != nil {
		t.Fatal(err)
	}
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("buffered changed state: %+v", st)
	}
	// So does a current report that is older than the evaluated one.
	if _, err := e.c.IngestReport(id, model.AgentReport{Time: t0 - 30, Checks: []model.Check{diskCheck(99)}}); err != nil {
		t.Fatal(err)
	}
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("older report changed state: %+v", st)
	}
	series, _ := e.st.SeriesList(id)
	if len(series) != 1 || series[0].LastValue != 40 {
		t.Fatalf("series: %+v", series)
	}
	pts, _ := e.st.Query([]int64{series[0].ID}, t0-3600, t0+60, 60)
	if len(pts[series[0].ID]) != 3 {
		t.Fatalf("history: %+v", pts)
	}

	// Silence: max(3×60 s, 180 s).
	e.clk.Add(2 * time.Minute)
	e.c.checkSilence()
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("too early: %+v", st)
	}
	e.clk.Add(2 * time.Minute)
	e.c.checkSilence()
	st := e.c.Status(id)
	reach, _ := findCheck(st, "reach")
	disk, _ := findCheck(st, "disk:/")
	if st.State != model.StateCrit || !strings.HasPrefix(reach.Message, "Agent meldet sich nicht (seit ") ||
		disk.State != model.StateUnknown || disk.Message != "Keine aktuellen Daten" {
		t.Fatalf("silent: %+v", st)
	}
	e.wait()
	if got := e.rec.titles(); len(got) != 1 || got[0] != "Kritisch: db01 – Erreichbarkeit" {
		t.Fatalf("notifications: %v", got)
	}
	// Back again.
	e.report(t, id, diskCheck(40))
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("back: %+v", st)
	}
}

func TestSaveDeviceValidation(t *testing.T) {
	e := newEnv(t, Options{})
	cases := []struct {
		d    model.Device
		keys []string
	}{
		{model.Device{Type: "x"}, []string{"name", "type"}},
		{model.Device{Name: "a", Type: model.TypeAgent}, []string{"type"}},
		{model.Device{Name: "a", Type: model.TypeProxmox, Interval: 5,
			Proxmox: &model.ProxmoxConfig{TokenID: "root", Port: 70000, WatchGuests: "some"}},
			[]string{"host", "interval", "proxmox.token_id", "proxmox.secret", "proxmox.port", "proxmox.watch_guests"}},
		{model.Device{Name: "a", Type: model.TypeSNMP, Host: "sw", SNMP: &model.SNMPConfig{Version: "3", SecLevel: "authPriv",
			AuthPass: "kurz", OIDs: []model.CustomOID{{OID: "1.3.x", Name: ""}}}},
			[]string{"snmp.user", "snmp.auth_pass", "snmp.priv_pass", "snmp.oids.0.oid", "snmp.oids.0.name"}},
		{model.Device{Name: "a", Type: model.TypeNet, Host: "h",
			Checks:     []model.NetCheck{{Type: model.NetHTTP, Target: "kein-url"}},
			Thresholds: []model.Threshold{{Metric: ""}, {Metric: "cpu.usage", Warn: ptr(90), Crit: ptr(80), Count: -1}}},
			[]string{"checks.0.target", "thresholds.0.metric", "thresholds.0.warn", "thresholds.1.crit", "thresholds.1.count"}},
		{model.Device{Name: "a", Type: model.TypeNet}, []string{"host"}},
	}
	for i, tc := range cases {
		_, fields, err := e.c.SaveDevice(tc.d)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		for _, k := range tc.keys {
			if fields[k] == "" {
				t.Errorf("%d: missing error %q in %v", i, k, fields)
			}
		}
		if len(fields) != len(tc.keys) {
			t.Errorf("%d: unexpected errors %v", i, fields)
		}
	}
	if len(e.c.Devices()) != 0 {
		t.Fatal("invalid devices were stored")
	}
	if _, _, err := e.c.SaveDevice(model.Device{ID: "nope", Name: "a", Type: model.TypeNet, Host: "h"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestSaveDeviceSecretsAndChecks(t *testing.T) {
	e := newEnv(t, Options{})
	d, fields, err := e.c.SaveDevice(model.Device{Name: " pve ", Type: model.TypeProxmox, Host: "10.0.0.1", Enabled: true,
		Proxmox: &model.ProxmoxConfig{TokenID: "pk95mon@pve!monitoring", Secret: "geheim", Fingerprint: strings.Repeat("ab", 32)},
		Checks: []model.NetCheck{{Type: model.NetPing}, {ID: "same01", Type: model.NetTCP, Target: "10.0.0.1:22"},
			{ID: "same01", Type: model.NetTCP, Target: "10.0.0.1:8006"}}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	if d.ID == "" || d.Name != "pve" || d.Proxmox.Port != 8006 || d.Proxmox.WatchGuests != "onboot" ||
		!strings.HasPrefix(d.Proxmox.Fingerprint, "AB:AB:") || d.Created == 0 {
		t.Fatalf("saved: %+v %+v", d, d.Proxmox)
	}
	ids := map[string]bool{}
	for _, nc := range d.Checks {
		if len(nc.ID) < 6 || ids[nc.ID] {
			t.Fatalf("check ids: %+v", d.Checks)
		}
		ids[nc.ID] = true
	}
	if !ids["same01"] {
		t.Fatalf("given id dropped: %+v", d.Checks)
	}
	// Empty secret keeps the stored one.
	d.Proxmox.Secret = ""
	d.Name = "pve1"
	d2, fields, err := e.c.SaveDevice(d)
	if err != nil || len(fields) > 0 || d2.Proxmox.Secret != "geheim" || d2.ID != d.ID || d2.Created != d.Created {
		t.Fatalf("keep secret: %v %v %+v", err, fields, d2.Proxmox)
	}
	var stored model.Device
	if err := e.st.Get(KindDevice, d.ID, &stored); err != nil || stored.Proxmox.Secret != "geheim" || stored.Name != "pve1" {
		t.Fatalf("stored: %v %+v", err, stored)
	}
	// Type cannot change.
	d2.Type = model.TypeNet
	if _, fields, _ = e.c.SaveDevice(d2); fields["type"] == "" {
		t.Fatalf("type change: %v", fields)
	}

	// SNMP community kept.
	s, _, _ := e.c.SaveDevice(model.Device{Name: "sw", Type: model.TypeSNMP, Host: "sw", SNMP: &model.SNMPConfig{Community: "c0mm"}})
	s.SNMP.Community = ""
	s, fields, _ = e.c.SaveDevice(s)
	if len(fields) > 0 || s.SNMP.Community != "c0mm" || s.SNMP.Version != "2c" || s.SNMP.Port != 161 {
		t.Fatalf("snmp: %v %+v", fields, s.SNMP)
	}

	// Agent devices can be edited without losing their facts.
	id := e.agentDevice(t, "ag", func(d *model.Device) {
		d.Agent.Platform, d.Agent.LastSeen = "debian", 12345
	})
	ag, _ := e.c.Device(id)
	ag.Name, ag.Group = "Agent 1", "Server"
	ag.Agent = &model.AgentConfig{Services: []string{" nginx ", "", "nginx", "ssh"}}
	ag, fields, err = e.c.SaveDevice(ag)
	if err != nil || len(fields) > 0 || ag.Agent.Platform != "debian" || ag.Agent.LastSeen != 12345 ||
		strings.Join(ag.Agent.Services, ",") != "nginx,ssh" || ag.Agent.AgentID != "ag-m" {
		t.Fatalf("agent edit: %v %v %+v", err, fields, ag.Agent)
	}
	if r := e.report(t, id); strings.Join(r.Services, ",") != "nginx,ssh" {
		t.Fatalf("reply services: %+v", r)
	}
}

func TestSchedulerEndToEnd(t *testing.T) {
	pve := fakepve.New()
	defer pve.Close()
	sw := fakesnmp.NewSwitch(8)
	e := newEnv(t, Options{})
	e.c.Now = time.Now
	e.c.tm = timings{silence: 50 * time.Millisecond, rollup: 100 * time.Millisecond, prune: time.Hour,
		persist: 50 * time.Millisecond, maxSleep: 100 * time.Millisecond, jitter: 50 * time.Millisecond}
	e.c.SNMP.Dial = fakesnmp.Dialer(map[string]*fakesnmp.Device{"10.0.0.2": sw})
	cfg := pve.Config()
	if err := e.c.ImportDevice(model.Device{ID: "pve", Name: "PVE", Type: model.TypeProxmox, Host: pve.Host(), Enabled: true,
		Interval: 1, Proxmox: &cfg}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.ImportDevice(model.Device{ID: "sw", Name: "Switch", Type: model.TypeSNMP, Host: "10.0.0.2", Enabled: true,
		Interval: 1, SNMP: &model.SNMPConfig{Version: "2c", Community: "public"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.ImportDevice(model.Device{ID: "off", Name: "Aus", Type: model.TypeSNMP, Host: "10.0.0.3", Interval: 1,
		SNMP: &model.SNMPConfig{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.c.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		p, s := e.c.Status("pve"), e.c.Status("sw")
		polls := func(id string) int64 { return e.c.Status(id).LastPoll }
		if p.LastOK > 0 && s.LastOK > 0 && len(p.Checks) > 5 && hasMetric(e, "sw", "if.rx") && polls("pve") > p.LastOK-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no polls: pve %+v\nswitch %+v", p, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := e.c.Status("off"); st.LastPoll != 0 {
		t.Fatalf("disabled device polled: %+v", st)
	}
	if _, ok := findCheck(e.c.Status("pve"), "pve:cluster"); !ok {
		t.Fatalf("cluster check missing")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
	sts, _ := e.st.LoadStatuses()
	if sts["pve"].LastOK == 0 || sts["sw"].LastOK == 0 {
		t.Fatalf("statuses not persisted: %v", sts)
	}
}

func hasMetric(e *env, id, name string) bool {
	list, _ := e.st.SeriesList(id)
	for _, s := range list {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestSummaryOverviewCounts(t *testing.T) {
	e := newEnv(t, Options{Instance: "Kunde A", PublicURL: "https://mon.example/"})
	ok := e.agentDevice(t, "ok", func(d *model.Device) { d.Group = "B" })
	crit := e.agentDevice(t, "crit", func(d *model.Device) { d.Group = "A" })
	maint := e.agentDevice(t, "maint", nil)
	off := e.agentDevice(t, "off", func(d *model.Device) { d.Enabled = false })
	e.agentDevice(t, "new", nil)
	e.report(t, ok, diskCheck(10))
	e.report(t, crit, diskCheck(99))
	e.report(t, maint, diskCheck(99))
	e.report(t, off, diskCheck(99))
	if _, err := e.c.SetMaintenance(maint, e.clk.Now().Add(time.Hour).Unix(), ""); err != nil {
		t.Fatal(err)
	}
	ov, err := e.c.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{OK: 1, Crit: 1, Unknown: 1, Maintenance: 1, Disabled: 1}
	if ov.Counts != want {
		t.Fatalf("counts %+v, want %+v", ov.Counts, want)
	}
	if len(ov.Devices) != 5 || ov.Devices[0].ID != "crit" || ov.Devices[1].ID != "ok" || len(ov.Alerts) != 1 ||
		len(ov.Events) == 0 || len(ov.Events) > 10 || strings.Join(ov.Groups, ",") != "A,B" {
		t.Fatalf("overview: %+v", ov)
	}
	if r := ov.Devices[0]; r.State != model.StateCrit || r.Checks.Crit != 1 || r.Checks.OK != 2 || r.Disk == nil { // reach and discovery ok
		t.Fatalf("row: %+v", r)
	}
	// Values older than 10 minutes are not shown.
	e.clk.Add(11 * time.Minute)
	if r := e.c.Rows()[0]; r.Disk != nil {
		t.Fatalf("old value shown: %+v", r)
	}

	s := e.c.Summary()
	if s.Instance != "Kunde A" || s.URL != "https://mon.example" || s.Version != "1.2.0" || s.State != model.StateCrit ||
		s.Counts["crit"] != 1 || s.Counts["maintenance"] != 1 || s.Counts["disabled"] != 1 || len(s.Devices) != 5 || len(s.Alerts) != 1 {
		t.Fatalf("summary: %+v", s)
	}
	ds, err := e.c.DeviceSummary(maint)
	if err != nil || !ds.Maintenance || ds.Checks["crit"] != 1 || ds.LastSeen == 0 {
		t.Fatalf("device summary: %v %+v", err, ds)
	}
	if _, err := e.c.DeviceSummary("x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestCentralPush(t *testing.T) {
	var mu sync.Mutex
	status, calls := http.StatusInternalServerError, 0
	var got model.Summary
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	e := newEnv(t, Options{})
	e.c.settings.Central = CentralSettings{Enabled: true, URL: srv.URL, Token: "tok", Interval: 5}
	ctx := context.Background()
	count := func(title string) int {
		evs, _ := e.st.Events(ctx, "", 0, 100)
		n := 0
		for _, ev := range evs {
			if ev.Title == title {
				n++
			}
		}
		return n
	}
	e.c.pushCentral(ctx)
	e.c.pushCentral(ctx)
	if n := count("Übertragung an die Zentrale fehlgeschlagen"); n != 1 || calls != 2 {
		t.Fatalf("failure events %d, calls %d", n, calls)
	}
	mu.Lock()
	status = http.StatusNoContent
	mu.Unlock()
	e.c.pushCentral(ctx)
	e.c.pushCentral(ctx)
	if n := count("Übertragung an die Zentrale funktioniert wieder"); n != 1 {
		t.Fatalf("recovery events %d", n)
	}
	if auth != "Bearer tok" || got.Instance != "Testkunde" || got.Counts == nil {
		t.Fatalf("request: %q %+v", auth, got)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.c.Settings()
	if s.General.RetentionRawDays != store.DefaultRawDays || s.Central.Interval != 5 || !s.Agent.AutoUpdate ||
		s.Notify.MinSeverity != "warning" {
		t.Fatalf("defaults: %+v", s)
	}
	s.Notify.HookToken = "geheim"
	s.Central.Token = "ct"
	if f, err := e.c.SaveSettings(s); err != nil || len(f) > 0 {
		t.Fatal(err, f)
	}
	s.Notify.HookToken, s.Central.Token = "", ""
	s.General.RetentionRawDays = 0
	if f, err := e.c.SaveSettings(s); err != nil || len(f) > 0 {
		t.Fatal(err, f)
	}
	got := e.c.Settings()
	if got.Notify.HookToken != "geheim" || got.Central.Token != "ct" || got.General.RetentionRawDays != store.DefaultRawDays {
		t.Fatalf("kept: %+v", got)
	}
	s.Central = CentralSettings{Enabled: true, URL: "ftp://x", Interval: 99999}
	s.Thresholds = []model.Threshold{{Metric: "cpu.usage"}}
	f, _ := e.c.SaveSettings(s)
	for _, k := range []string{"central.url", "central.interval", "thresholds.0.warn"} {
		if f[k] == "" {
			t.Errorf("missing %s in %v", k, f)
		}
	}
	// Reload from the store.
	c2, err := New(e.st, Options{AgentDir: t.TempDir()})
	if err != nil || c2.Settings().Notify.HookToken != "geheim" {
		t.Fatalf("reload: %v %+v", err, c2.Settings())
	}
	k1 := e.c.EnrollKey()
	k2, err := e.c.RotateEnrollKey()
	if err != nil || k1 == k2 || !e.c.checkEnrollKey(k2) || e.c.checkEnrollKey(k1) {
		t.Fatalf("rotate: %v", err)
	}
}

func TestNetDeviceImplicitPing(t *testing.T) {
	d := model.Device{Type: model.TypeNet, Host: "127.0.0.1", Enabled: true}
	ncs := netChecksOf(d)
	if len(ncs) != 1 || ncs[0].Key() != "net:ping" || ncs[0].Type != model.NetPing {
		t.Fatalf("implicit ping: %+v", ncs)
	}
	d.Checks = []model.NetCheck{{ID: "x", Type: model.NetTCP, Target: "127.0.0.1:1"}}
	if ncs := netChecksOf(d); len(ncs) != 1 || ncs[0].ID != "x" {
		t.Fatalf("explicit: %+v", ncs)
	}
	if pollable(model.Device{Type: model.TypeAgent, Enabled: true}) || !pollable(model.Device{Type: model.TypeSNMP, Enabled: true}) {
		t.Fatal("pollable")
	}
}

func TestTestNetCheck(t *testing.T) {
	e := newEnv(t, Options{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hallo welt")) }))
	defer srv.Close()
	ch := e.c.TestNetCheck(context.Background(), "", model.NetCheck{Type: model.NetHTTP, Target: srv.URL, Keyword: "welt"})
	if ch.State != model.StateOK || ch.Key != "net:test" {
		t.Fatalf("check: %+v", ch)
	}
}
