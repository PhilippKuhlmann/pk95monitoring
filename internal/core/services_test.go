package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// winReport sends a report of a Windows agent.
func (e *env) winReport(t *testing.T, id string, checks ...model.Check) model.AgentReply {
	t.Helper()
	r, err := e.c.IngestReport(id, model.AgentReport{OS: "windows", Arch: "amd64", Version: "1.2.0", Time: e.clk.Now().Unix(), Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCentralServices(t *testing.T) {
	e := newEnv(t, Options{})
	win := e.agentDevice(t, "dc01", func(d *model.Device) { d.Agent.OS = "windows"; d.AutoAccept = false })
	lin := e.agentDevice(t, "db01", func(d *model.Device) { d.AutoAccept = false })
	e.winReport(t, win, diskCheck(40))
	e.report(t, lin, diskCheck(40))

	// Validation: lists are trimmed and de-duplicated; invalid entries are field errors.
	s := e.c.Settings()
	if s.Agent.WindowsServices == nil || s.Agent.LinuxServices == nil {
		t.Fatal("lists are nil")
	}
	s.Agent.WindowsServices = []string{" veeam* ", "veeam*", "MSSQL$*", ""}
	s.Agent.LinuxServices = []string{"veeam*", "ssh"}
	if f, err := e.c.SaveSettings(s); err != nil || len(f) > 0 {
		t.Fatal(f, err)
	}
	got := e.c.Settings().Agent
	if !slices.Equal(got.WindowsServices, []string{"veeam*", "MSSQL$*"}) || !slices.Equal(got.LinuxServices, []string{"veeam*", "ssh"}) {
		t.Fatalf("saved %+v", got)
	}
	bad := e.c.Settings()
	bad.Agent.LinuxServices = []string{strings.Repeat("x", 300)}
	if f, _ := e.c.SaveSettings(bad); f["agent.linux_services"] == "" {
		t.Fatalf("long entry accepted: %v", f)
	}

	// Each agent gets the list of its OS with every reply.
	e.clk.Add(time.Minute)
	if r := e.winReport(t, win, diskCheck(40), metricCheck("svc:VeeamBackupSvc", "Dienst Veeam Backup Service")); !slices.Equal(r.CentralServices, []string{"veeam*", "MSSQL$*"}) {
		t.Fatalf("windows reply %v", r.CentralServices)
	}
	if r := e.report(t, lin, diskCheck(40)); !slices.Equal(r.CentralServices, []string{"veeam*", "ssh"}) {
		t.Fatalf("linux reply %v", r.CentralServices)
	}
	// Services the new selection brings are expected, not new.
	if cs := mustCheck(t, e.c.Status(win), "svc:VeeamBackupSvc"); cs.Mode != "" {
		t.Fatalf("central service marked new: %+v", cs)
	}
	// Later ones are discovered as usual.
	e.clk.Add(time.Minute)
	e.winReport(t, win, diskCheck(40), metricCheck("svc:VeeamBackupSvc", "Dienst Veeam Backup Service"), metricCheck("svc:VeeamNFSSvc", "Dienst Veeam vPower NFS"))
	if cs := mustCheck(t, e.c.Status(win), "svc:VeeamNFSSvc"); cs.Mode != model.ModeNew {
		t.Fatalf("later service: %+v", cs)
	}

	// Deselected services (central list emptied) are dropped, not reported as vanished.
	s = e.c.Settings()
	s.Agent.WindowsServices = nil
	if f, err := e.c.SaveSettings(s); err != nil || len(f) > 0 {
		t.Fatal(f, err)
	}
	for range 3 {
		e.clk.Add(time.Minute)
		e.winReport(t, win, diskCheck(40))
	}
	if st := e.c.Status(win); len(st.Vanished) != 0 || mustCheck(t, st, model.DiscoveryKey).State != model.StateOK {
		t.Fatalf("deselected services vanished: %+v", st.Vanished)
	}

	// Device lists: ignore entries are validated, too.
	d, _ := e.c.Device(win)
	d.Agent.IgnoreServices = []string{"a\nb"}
	if _, f, _ := e.c.SaveDevice(d); f["agent.ignore_services"] == "" {
		t.Fatalf("invalid ignore entry accepted: %v", f)
	}
}
