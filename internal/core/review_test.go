package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// A single failed poll is shown as warn but must not alert; the second one in a row does.
func TestReachSingleFailureDoesNotAlert(t *testing.T) {
	e := newEnv(t, Options{})
	sw := fakesnmp.NewSwitch(4)
	e.c.SNMP.Dial = fakesnmp.Dialer(map[string]*fakesnmp.Device{"10.0.0.2": sw})
	d, fields, err := e.c.SaveDevice(model.Device{Name: "Switch", Type: model.TypeSNMP, Host: "10.0.0.2", Enabled: true,
		SNMP: &model.SNMPConfig{Community: "public"}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	ctx := context.Background()
	e.c.PollNow(ctx, d.ID)
	for range 3 { // flapping: fail once, recover
		sw.SetReachable(false)
		if st, _ := e.c.PollNow(ctx, d.ID); st.State != model.StateWarn {
			t.Fatalf("one failure: %+v", st)
		}
		sw.SetReachable(true)
		e.c.PollNow(ctx, d.ID)
	}
	e.wait()
	if active, _ := e.st.ActiveAlerts(); len(active) != 0 || len(e.rec.titles()) != 0 {
		t.Fatalf("single failures alerted: %+v %v", active, e.rec.titles())
	}
	sw.SetReachable(false)
	e.c.PollNow(ctx, d.ID)
	e.c.PollNow(ctx, d.ID)
	sw.SetReachable(true)
	e.c.PollNow(ctx, d.ID)
	e.wait()
	if got := e.rec.titles(); !slices.Equal(got, []string{"Kritisch: Switch – Erreichbarkeit", "Wieder OK: Switch – Erreichbarkeit"}) {
		t.Fatalf("unreachable: %v", got)
	}
}

// Who was told "Kritisch" must also get "Wieder OK", even when the alert went back to warn
// before it closed and only critical notifications are wanted.
func TestRecoveryNotifiedAfterDeescalation(t *testing.T) {
	e := newEnv(t, Options{})
	e.c.settings.Notify.MinSeverity = "critical"
	id := e.agentDevice(t, "web01", nil)
	e.report(t, id, diskCheck(97))
	e.report(t, id, diskCheck(90))
	e.report(t, id, diskCheck(40))
	e.wait()
	if got := e.rec.titles(); !slices.Equal(got, []string{"Kritisch: web01 – Festplatte /", "Wieder OK: web01 – Festplatte /"}) {
		t.Fatalf("notifications: %v", got)
	}
	// A warn-only alert stays silent with that filter, also when it closes.
	e.rec.reset()
	e.report(t, id, diskCheck(90))
	e.report(t, id, diskCheck(40))
	e.wait()
	if got := e.rec.titles(); len(got) != 0 {
		t.Fatalf("warn alert notified: %v", got)
	}
}

// An agent that keeps reporting its old version must not get the update with every report.
func TestUpdateOfferNoLoop(t *testing.T) {
	dir := t.TempDir()
	writeAgentBinary(t, dir)
	e := newEnv(t, Options{AgentDir: dir, Version: "1.2.0"})
	id := e.agentDevice(t, "a", nil)
	send := func(ver string) *model.AgentUpdate {
		r, err := e.c.IngestReport(id, model.AgentReport{OS: "linux", Arch: "amd64", Version: ver, Time: e.clk.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return r.Update
	}
	if send("1.1.0") == nil {
		t.Fatal("no update offered")
	}
	for range 5 {
		e.clk.Add(time.Minute)
		if send("1.1.0") != nil {
			t.Fatal("update offered again right away")
		}
	}
	e.clk.Add(7 * time.Hour)
	if send("1.1.0") == nil {
		t.Fatal("update not retried after the back-off")
	}
	// Another old version (e.g. a manual downgrade) gets it at once; the current one never.
	if send("1.0.0") == nil {
		t.Fatal("other version not offered")
	}
	if send("1.2.0") != nil {
		t.Fatal("current version offered")
	}
}

// After a restart agents get a full silence period before they count as silent, even when
// their LastSeen is old (server was down).
func TestSilenceGraceAfterRestart(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "db01", func(d *model.Device) { d.Agent.LastSeen = e.clk.Now().Add(-time.Hour).Unix() })
	e.c.start()
	e.clk.Add(2 * time.Minute)
	e.c.checkSilence()
	if st := e.c.Status(id); st.State == model.StateCrit {
		t.Fatalf("silent right after the start: %+v", st)
	}
	e.clk.Add(2 * time.Minute)
	e.c.checkSilence()
	if st := e.c.Status(id); st.State != model.StateCrit {
		t.Fatalf("not silent after the grace period: %+v", st)
	}
}

// Restored alerts are not re-notified at once after a restart.
func TestRestartDoesNotRenotifyAtOnce(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "db01", nil)
	e.report(t, id, diskCheck(99))
	e.c.persist(true)
	e.wait()
	e.clk.Add(5 * time.Hour)

	c2, err := New(e.st, Options{AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	c2.Now, c2.Notify = e.clk.Now, rec.send
	c2.settings.Notify.HookEnabled, c2.settings.Notify.HookURL = true, "http://127.0.0.1:1/hook"
	c2.settings.General.RenotifyHours = 1
	c2.start()
	c2.renotify()
	c2.notifyWG.Wait()
	if len(rec.titles()) != 0 {
		t.Fatalf("re-notified at start: %v", rec.titles())
	}
	e.clk.Add(61 * time.Minute)
	c2.renotify()
	c2.notifyWG.Wait()
	if got := rec.titles(); !slices.Equal(got, []string{"Weiterhin Kritisch: db01 – Festplatte /"}) {
		t.Fatalf("renotify: %v", got)
	}
	// The restored status keeps the alert: a report with the same value raises nothing new.
	rec.reset()
	if _, err := c2.IngestReport(id, model.AgentReport{Time: e.clk.Now().Unix(), Checks: []model.Check{diskCheck(99)}}); err != nil {
		t.Fatal(err)
	}
	c2.notifyWG.Wait()
	if active, _ := e.st.ActiveAlerts(); len(active) != 1 || len(rec.titles()) != 0 {
		t.Fatalf("after restart: %+v %v", active, rec.titles())
	}
}

// The device message does not repeat the check name when the reason starts with it.
func TestDeviceMessageNoDuplicateName(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "drucker", nil)
	e.report(t, id, model.Check{Key: "snmp:toner:Magenta Toner", Name: "Magenta Toner", Group: "Drucker",
		State: model.StateOK, Message: "7 %",
		Metrics: []model.Metric{{Name: "printer.toner", Instance: "Magenta Toner", Value: 7, Unit: "%"}}})
	if st := e.c.Status(id); st.State != model.StateWarn || st.Message != "Magenta Toner 7 % ≤ 10 %" {
		t.Fatalf("message: %q %q", st.State, st.Message)
	}
}

func TestInstanceLabel(t *testing.T) {
	for _, c := range [][3]string{
		{"Toner", "Magenta Toner", "Magenta Toner"},
		{"Toner", "Black", "Toner Black"},
		{"Festplattenbelegung", "/", "Festplattenbelegung /"},
		{"CPU-Last", "", "CPU-Last"},
	} {
		if got := instanceLabel(c[0], c[1]); got != c[2] {
			t.Errorf("instanceLabel(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// SNMP v3 secrets are kept when the form sends them empty.
func TestSaveDeviceKeepsSNMPv3Secrets(t *testing.T) {
	e := newEnv(t, Options{})
	d, fields, err := e.c.SaveDevice(model.Device{Name: "nas", Type: model.TypeSNMP, Host: "10.0.0.30",
		SNMP: &model.SNMPConfig{Version: "3", User: "mon", SecLevel: "authPriv", AuthPass: "auth-geheim", PrivPass: "priv-geheim"}})
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	d.SNMP.AuthPass, d.SNMP.PrivPass, d.SNMP.PrivProto = "", "", "AES256"
	d, fields, err = e.c.SaveDevice(d)
	if err != nil || len(fields) > 0 || d.SNMP.AuthPass != "auth-geheim" || d.SNMP.PrivPass != "priv-geheim" || d.SNMP.PrivProto != "AES256" {
		t.Fatalf("keep: %v %v %+v", err, fields, d.SNMP)
	}
	var stored model.Device
	if err := e.st.Get(KindDevice, d.ID, &stored); err != nil || stored.SNMP.AuthPass != "auth-geheim" || stored.SNMP.PrivPass != "priv-geheim" {
		t.Fatalf("stored: %v %+v", err, stored.SNMP)
	}
}

// Writes that happen outside the engine lock (metrics, persisted statuses and LastSeen) must
// not bring back the data of a device that was deleted meanwhile.
func TestDeletedDeviceLeavesNothing(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "gone", nil)
	e.report(t, id, diskCheck(40))
	e.clk.Add(time.Minute)
	e.report(t, id, diskCheck(40)) // LastSeen newer than saved, status dirty
	if err := e.c.DeleteDevice(id); err != nil {
		t.Fatal(err)
	}
	// What a poll or report that was already past its lookup would still do:
	e.c.writeMetrics(id, e.clk.Now().Unix(), []model.Check{diskCheck(50)})
	e.c.persist(true)

	if list, _ := e.st.SeriesList(id); len(list) != 0 {
		t.Fatalf("series left: %+v", list)
	}
	if sts, _ := e.st.LoadStatuses(); sts[id].DeviceID != "" {
		t.Fatalf("status left: %+v", sts[id])
	}
	var d model.Device
	if err := e.st.Get(KindDevice, id, &d); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device written back: %v %+v", err, d)
	}
}

// Save, delete, poll, report and persist at the same time (run with -race).
func TestConcurrentSaveDeletePollReport(t *testing.T) {
	e := newEnv(t, Options{})
	e.c.Now = time.Now
	sw := fakesnmp.NewSwitch(4)
	e.c.SNMP.Dial = fakesnmp.Dialer(map[string]*fakesnmp.Device{"10.0.0.2": sw})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 15 {
				d, fields, err := e.c.SaveDevice(model.Device{Name: fmt.Sprintf("sw%d-%d", i, j), Type: model.TypeSNMP,
					Host: "10.0.0.2", Enabled: true, SNMP: &model.SNMPConfig{Community: "public"},
					Checks: []model.NetCheck{{Type: model.NetTCP, Target: "127.0.0.1:1"}}})
				if err != nil || len(fields) > 0 {
					t.Error(err, fields)
					return
				}
				var inner sync.WaitGroup
				inner.Go(func() { e.c.PollNow(ctx, d.ID) })
				inner.Go(func() {
					d.Name += " neu"
					d.SNMP.Community = ""
					if _, _, err := e.c.SaveDevice(d); err != nil && !errors.Is(err, store.ErrNotFound) {
						t.Error(err)
					}
				})
				inner.Go(func() { e.c.persist(j%3 == 0) })
				inner.Go(func() { _ = e.c.Rows(); _ = e.c.Summary() })
				inner.Go(func() {
					if err := e.c.DeleteDevice(d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
						t.Error(err)
					}
				})
				inner.Wait()
			}
		})
	}
	agent := e.agentDevice(t, "ag", nil)
	wg.Go(func() {
		for range 60 {
			if _, err := e.c.IngestReport(agent, model.AgentReport{Time: time.Now().Unix(), Version: "1.0.0",
				Checks: []model.Check{diskCheck(91)}}); err != nil {
				t.Error(err)
				return
			}
			e.c.checkSilence()
			e.c.syncAllAlerts()
		}
	})
	wg.Wait()
	e.wait()
	e.c.persist(true)
	if n := len(e.c.Devices()); n != 1 {
		t.Fatalf("devices left: %d", n)
	}
	sts, _ := e.st.LoadStatuses()
	for id := range sts {
		if id != agent {
			t.Errorf("status of deleted device %s left", id)
		}
	}
	list, _ := e.st.LatestSeries("if.rx", "tcp.time", "disk.usage")
	for _, s := range list {
		if s.DeviceID != agent {
			t.Errorf("series of deleted device left: %+v", s)
		}
	}
	devs, _ := store.List[model.Device](e.st, KindDevice)
	if len(devs) != 1 {
		t.Errorf("stored devices: %d", len(devs))
	}
}

// Changing the central settings makes the next tick push instead of waiting for the old interval.
func TestCentralSettingsResetSchedule(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.c.Settings()
	s.Central = CentralSettings{Enabled: true, URL: "https://zentrale.example/push", Token: "t", Interval: 5}
	if fields, err := e.c.SaveSettings(s); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	if !e.c.centralReset.Load() {
		t.Fatal("central change not noticed")
	}
}

// A server that was suspended (gap between silence checks) must not declare every agent
// silent on wake-up; the agents get the normal grace period again.
func TestSilenceAfterSuspend(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.agentDevice(t, "web01", nil)
	e.report(t, id, diskCheck(40))
	e.c.checkSilence() // first tick
	e.clk.Add(30 * time.Second)
	e.c.checkSilence()

	e.clk.Add(2 * time.Hour) // suspended
	e.c.checkSilence()
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("silent right after wake-up: %+v", st)
	}
	e.clk.Add(30 * time.Second)
	e.c.checkSilence()
	if st := e.c.Status(id); st.State != model.StateOK {
		t.Fatalf("silent within grace: %+v", st)
	}
	// Still no report after the grace period: now it is silent.
	for i := 0; i < 8; i++ {
		e.clk.Add(30 * time.Second)
		e.c.checkSilence()
	}
	if st := e.c.Status(id); st.State != model.StateCrit {
		t.Fatalf("not silent after grace: %+v", st)
	}
}
