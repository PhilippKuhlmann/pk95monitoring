package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

func TestStatusPageSave(t *testing.T) {
	e := newEnv(t, Options{})
	srv := e.agentDevice(t, "srv-alpha", func(d *model.Device) { d.Group = "Server" })

	_, errs, err := e.c.SaveStatusPage(StatusPage{Enabled: true, NoticeLevel: "loud"})
	if err != nil || errs["components"] == "" || errs["notice_level"] == "" {
		t.Fatalf("enabled without services: %v %v", errs, err)
	}
	_, errs, _ = e.c.SaveStatusPage(StatusPage{Components: []StatusComponent{{Mode: "sometimes", Devices: []string{"gone"}}}})
	for _, k := range []string{"components.0.name", "components.0.mode", "components.0.targets"} {
		if errs[k] == "" {
			t.Errorf("no error for %s: %v", k, errs)
		}
	}

	p, errs, err := e.c.SaveStatusPage(StatusPage{Enabled: true, Title: "  Status   Muster ", Notice: "Provider gestört",
		Components: []StatusComponent{{Name: "Dateiablage", Devices: []string{srv, srv, "gone"}, Folders: []string{" Server/ ", "Server"}}}})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save: %v %v", errs, err)
	}
	cp := p.Components[0]
	if len(p.Token) != statusTokenLen || p.Title != "Status Muster" || p.NoticeLevel != "info" || p.NoticeAt == 0 ||
		cp.Mode != StatusOutage || cp.ID == "" || len(cp.Devices) != 1 || len(cp.Folders) != 1 {
		t.Fatalf("saved %+v", p)
	}
	if !e.c.StatusTokenValid(p.Token) || e.c.StatusTokenValid("") || e.c.StatusTokenValid(p.Token[1:]) {
		t.Fatal("token check")
	}
	// the token survives a save (the client cannot set it), the notice time a save without change
	e.clk.Add(time.Minute)
	p2, _, _ := e.c.SaveStatusPage(StatusPage{Enabled: true, Token: "mine", Notice: "Provider gestört", Components: p.Components})
	if p2.Token != p.Token || p2.NoticeAt != p.NoticeAt || p2.Components[0].ID != cp.ID {
		t.Fatalf("second save %+v", p2)
	}
	r, err := e.c.RotateStatusToken()
	if err != nil || r.Token == p.Token || e.c.StatusTokenValid(p.Token) || !e.c.StatusTokenValid(r.Token) {
		t.Fatalf("rotate: %+v %v", r, err)
	}
	if lastEventBody(t, e, "Neuer Link für die Statusseite") == "" {
		t.Fatal("no event for the new link")
	}
	// switched off: the link shows nothing
	if _, _, err := e.c.SaveStatusPage(StatusPage{Components: p.Components}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.PublicStatus(context.Background(), r.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("public while off: %v", err)
	}
	// stored: a new engine on the same store has the page
	c2, err := New(e.st, e.c.Opt)
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.StatusPage(); got.Token != r.Token || len(got.Components) != 1 {
		t.Fatalf("reloaded %+v", got)
	}
}

func TestStatusPagePublic(t *testing.T) {
	e := newEnv(t, Options{}) // the clock is 2026-11-11 10:00
	old := func(group string) func(*model.Device) {
		return func(d *model.Device) { d.Group, d.Created = group, e.clk.Now().AddDate(0, 0, -60).Unix() }
	}
	a := e.agentDevice(t, "srv-alpha", old("Server/Nord"))
	b := e.agentDevice(t, "srv-beta", old("Server"))
	w := e.agentDevice(t, "web-gamma", old("Web"))
	ok := model.Check{Key: "svc", Name: "Dienst", Group: "Dienste", State: model.StateOK}
	crit := model.Check{Key: "svc", Name: "Dienst", Group: "Dienste", State: model.StateCrit, Message: "gestoppt"}
	warn := model.Check{Key: "http", Name: "Startseite", Group: "Web", State: model.StateWarn, Message: "langsam"}
	for _, id := range []string{a, b, w} {
		e.report(t, id, ok)
	}
	// an outage of alpha three days ago, 30 minutes
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local).Unix()
	aid, err := e.st.OpenAlert(model.Alert{DeviceID: a, DeviceName: a, CheckKey: reachKey, CheckName: "Erreichbarkeit", Severity: model.StateCrit, Started: t0})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CloseAlert(aid, t0+1800); err != nil {
		t.Fatal(err)
	}
	if _, errs, err := e.c.SaveWindow(MaintWindow{Name: "Patchen", Enabled: true, Public: true, Repeat: RepeatDaily, Start: "22:00",
		Minutes: 120, Folders: []string{"Server"}, Note: "Neustarts"}); err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if _, _, err := e.c.SaveWindow(MaintWindow{Name: "Intern", Enabled: true, Repeat: RepeatDaily, Start: "23:00",
		Minutes: 60, Devices: []string{w}}); err != nil {
		t.Fatal(err)
	}
	p, errs, err := e.c.SaveStatusPage(StatusPage{Enabled: true, Intro: "Hotline 123", Components: []StatusComponent{
		{Name: "Dateiablage", Note: "Laufwerk S:", Folders: []string{"Server"}},
		{Name: "Webseite", Mode: StatusAll, Devices: []string{w}},
	}})
	if err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	ctx := context.Background()
	get := func() PublicStatus {
		t.Helper()
		s, err := e.c.PublicStatus(ctx, p.Token)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	s := get()
	if s.State != PubOK || s.Text != "Alle Dienste betriebsbereit" || len(s.Components) != 2 || s.Intro != "Hotline 123" || s.Days != statusDays {
		t.Fatalf("all ok: %+v", s)
	}
	fs := s.Components[0]
	if fs.State != PubOK || fs.Members != 0 || len(fs.History) != statusDays || fs.History[statusDays-1].Day != "2026-11-11" {
		t.Fatalf("Dateiablage %+v", fs)
	}
	if d := fs.History[26]; d.Day != "2026-11-08" || d.State != PubCrit || d.Down != 1800 {
		t.Fatalf("outage day %+v", d)
	}
	if fs.Availability == nil || *fs.Availability > 99.99 || *fs.Availability < 99.9 {
		t.Fatalf("availability %v", fs.Availability)
	}
	if web := s.Components[1]; web.History[26].State != PubOK || web.Availability == nil || *web.Availability != 100 {
		t.Fatalf("Webseite %+v", web)
	}
	if len(s.Maintenance) != 1 || s.Maintenance[0].Name != "Patchen" || s.Maintenance[0].Running ||
		s.Maintenance[0].Start != time.Date(2026, 11, 11, 22, 0, 0, 0, time.Local).Unix() ||
		len(s.Maintenance[0].Components) != 1 || s.Maintenance[0].Components[0] != "Dateiablage" {
		t.Fatalf("maintenance %+v", s.Maintenance)
	}
	raw, _ := json.Marshal(s)
	for _, secret := range []string{a, b, w, "10.0.0.9", "Intern", "Erreichbarkeit", "gestoppt"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("public status names %q: %s", secret, raw)
		}
	}

	// a critical check is no outage of an "outage" service, a warning limits an "all" service;
	// one of two silent agents is a partial outage
	e.report(t, w, ok, warn)
	if s := get(); s.State != PubOK {
		t.Fatalf("cached view changed: %+v", s.State)
	}
	e.clk.Add(4 * time.Minute)
	e.report(t, a, crit)
	e.report(t, w, ok, warn)
	e.c.checkSilence() // beta has not reported for 4 minutes
	e.clk.Add(statusCacheTTL)
	e.report(t, a, crit)
	e.report(t, w, ok, warn)
	s = get()
	fs, web := s.Components[0], s.Components[1]
	if fs.State != PubWarn || fs.Text != "Teilweise gestört" || fs.Since == 0 {
		t.Fatalf("partial outage %+v", fs)
	}
	if web.State != PubWarn || web.Text != "Eingeschränkt" || web.History[statusDays-1].State != PubWarn {
		t.Fatalf("limited %+v", web)
	}
	if s.State != PubWarn || s.Text != "Einzelne Dienste eingeschränkt" {
		t.Fatalf("overall %+v", s)
	}
	if today := fs.History[statusDays-1]; today.State != PubCrit || today.Down == 0 {
		t.Fatalf("today %+v", today)
	}

	// maintenance by hand; the preview counts the devices
	if _, err := e.c.SetMaintenance(w, e.clk.Now().Add(time.Hour).Unix(), ""); err != nil {
		t.Fatal(err)
	}
	pv, err := e.c.StatusPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if web := pv.Components[1]; web.State != PubMaint || web.Until == 0 || pv.Components[0].Members != 2 {
		t.Fatalf("preview %+v", pv.Components)
	}
}

func TestOverallState(t *testing.T) {
	c := func(states ...string) []PublicComponent {
		out := []PublicComponent{}
		for _, s := range states {
			out = append(out, PublicComponent{State: s})
		}
		return out
	}
	for _, tc := range []struct {
		in    []PublicComponent
		state string
	}{
		{c(), PubUnknown}, {c(PubOK, PubUnknown), PubOK}, {c(PubOK, PubMaint), PubMaint},
		{c(PubMaint, PubWarn), PubWarn}, {c(PubCrit, PubOK), PubCrit}, {c(PubUnknown), PubUnknown},
	} {
		if s, _ := overallState(tc.in); s != tc.state {
			t.Errorf("%v: %s, want %s", tc.in, s, tc.state)
		}
	}
	if _, text := overallState(c(PubCrit, PubCrit)); text != "Größere Störung" {
		t.Errorf("all down: %s", text)
	}
}

// A device without a reachability check (network checks) is down in an "outage" service when
// it is critical, an agent with a critical check is not.
func TestStatusOutageWithoutReach(t *testing.T) {
	e := newEnv(t, Options{})
	net := e.agentDevice(t, "net-delta", nil)
	agent := e.agentDevice(t, "srv-alpha", nil)
	e.report(t, agent, model.Check{Key: "svc", Name: "Dienst", State: model.StateCrit})
	e.c.mu.Lock()
	e.c.rt[net].st = model.DeviceStatus{DeviceID: net, State: model.StateCrit, Since: 5,
		Checks: []model.CheckStatus{{Check: model.Check{Key: "net:http", Name: "Startseite", State: model.StateCrit}}}}
	e.c.mu.Unlock()
	s, err := e.c.buildStatus(context.Background(), StatusPage{Components: []StatusComponent{
		{Name: "Web", Mode: StatusOutage, Devices: []string{net}},
		{Name: "Server", Mode: StatusOutage, Devices: []string{agent}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if web := s.Components[0]; web.State != PubCrit || web.Since != 5 {
		t.Errorf("net device %+v", web)
	}
	if srv := s.Components[1]; srv.State != PubOK {
		t.Errorf("agent with a critical check %+v", srv)
	}
}
