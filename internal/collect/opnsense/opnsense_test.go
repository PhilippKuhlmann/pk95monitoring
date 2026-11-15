package opnsense_test

import (
	"context"
	"strings"
	"testing"

	"pk95monitoring/internal/collect/opnsense"
	"pk95monitoring/internal/fake/fakeopnsense"
	"pk95monitoring/internal/model"
)

func device(s *fakeopnsense.Server) model.Device {
	cfg := s.Config()
	return model.Device{ID: "fw", Name: "fw", Type: model.TypeOPNsense, Host: s.Host(), OPNsense: &cfg}
}

func collect(t *testing.T, c *opnsense.Collector, d model.Device) map[string]model.Check {
	t.Helper()
	checks, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]model.Check{}
	for _, ck := range checks {
		if _, dup := m[ck.Key]; dup {
			t.Errorf("duplicate key %s", ck.Key)
		}
		m[ck.Key] = ck
	}
	return m
}

func must(t *testing.T, m map[string]model.Check, key string) model.Check {
	t.Helper()
	ck, ok := m[key]
	if !ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		t.Fatalf("check %s missing (have %v)", key, keys)
	}
	return ck
}

func metric(ck model.Check, name string) (float64, bool) {
	for _, m := range ck.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}

func TestCollect(t *testing.T) {
	s := fakeopnsense.New()
	defer s.Close()
	c := opnsense.New()
	d := device(s)
	m := collect(t, c, d)

	sys := must(t, m, "opn:system")
	if sys.State != model.StateOK || !strings.HasPrefix(sys.Message, "OPNsense 25.7.6-amd64 · läuft seit 23 Tagen") ||
		sys.Facts["Name"] != "fw-filiale.kunde.local" || sys.Facts["Kernel"] == "" {
		t.Errorf("system: %+v", sys)
	}
	if up, ok := metric(sys, "uptime"); !ok || up < 23*86400 {
		t.Errorf("uptime %v", up)
	}
	if v, ok := metric(must(t, m, "opn:mem"), "mem.usage"); !ok || v < 30 || v > 38 {
		t.Errorf("mem.usage %v", v)
	}
	if v, ok := metric(must(t, m, "opn:disk"), "disk.usage"); !ok || v != 4 {
		t.Errorf("disk.usage %v", v)
	}
	if v, ok := metric(must(t, m, "opn:temp"), "temp"); !ok || v < 43 || v > 49 {
		t.Errorf("temp %v", v)
	}
	up := must(t, m, "updates")
	if up.State != model.StateWarn || up.Message != "25.7.6 · 3 Updates verfügbar (25.7.7)" {
		t.Errorf("updates: %+v", up)
	}
	if v, _ := metric(up, "updates.pending"); v != 3 {
		t.Errorf("updates.pending %v", v)
	}
	gw := must(t, m, "opn:gw:WAN_TELEKOM")
	if gw.State != model.StateOK || !strings.HasPrefix(gw.Message, "Online · ") || gw.Facts["Überwacht über"] != "1.1.1.1" {
		t.Errorf("gateway: %+v", gw)
	}
	if v, ok := metric(gw, "gw.latency"); !ok || v < 8 || v > 12 {
		t.Errorf("gw.latency %v", v)
	}
	if ip := must(t, m, "opn:ipsec:Filiale Süd"); ip.State != model.StateOK || !strings.HasPrefix(ip.Message, "verbunden · Schlüssel seit ") {
		t.Errorf("ipsec: %+v", ip)
	}
	if wg := must(t, m, "opn:wg:Standorte/Lager Ost"); wg.State != model.StateOK || wg.Message != "verbunden · Handshake vor 40 s" {
		t.Errorf("wireguard: %+v", wg)
	}
	if wg := must(t, m, "opn:wg:Standorte/Notebook GF"); wg.State != model.StateOK || wg.Message != "noch nie verbunden" {
		t.Errorf("wireguard road warrior: %+v", wg)
	}
	if carp := must(t, m, "opn:carp"); carp.State != model.StateOK || carp.Message != "Master · 2 virtuelle IPs" {
		t.Errorf("carp: %+v", carp)
	}
	if sv := must(t, m, "opn:svc:unbound"); sv.State != model.StateOK || sv.Name != "Dienst Unbound DNS" {
		t.Errorf("service: %+v", sv)
	}
	if p := must(t, m, "opn:perm"); p.State != model.StateOK {
		t.Errorf("perm: %+v", p)
	}

	// problems
	s.SetGateway("WAN_TELEKOM", true)
	s.SetTunnel(false)
	s.SetCARP("BACKUP")
	s.SetServiceStopped("unbound", true)
	s.SetUpdates(0, false)
	s.Forbid("/api/wireguard/")
	m = collect(t, c, d)
	want := map[string]string{"opn:gw:WAN_TELEKOM": model.StateCrit, "opn:gw:WAN_LTE": model.StateOK, "opn:ipsec:Filiale Süd": model.StateCrit,
		"opn:carp": model.StateOK, "opn:svc:unbound": model.StateCrit, "updates": model.StateUnknown, "opn:perm": model.StateWarn}
	for k, st := range want {
		if ck := must(t, m, k); ck.State != st {
			t.Errorf("%s: %s (%s), want %s", k, ck.State, ck.Message, st)
		}
	}
	if p := m["opn:perm"]; p.Message != "Keine Leserechte für: WireGuard" {
		t.Errorf("perm message %q", p.Message)
	}
	if _, ok := m["opn:wg:Standorte/Lager Ost"]; ok {
		t.Error("forbidden WireGuard still reported")
	}
	if m["opn:carp"].Message != "Backup · 2 virtuelle IPs" {
		t.Errorf("carp %q", m["opn:carp"].Message)
	}
	s.SetCARP("INIT")
	if m = collect(t, c, d); m["opn:carp"].State != model.StateCrit {
		t.Errorf("carp INIT: %+v", m["opn:carp"])
	}
}

func TestAuthAndTest(t *testing.T) {
	s := fakeopnsense.New()
	defer s.Close()
	cfg := s.Config()
	cfg.Secret = "falsch"
	if _, err := opnsense.Test(context.Background(), s.Host(), cfg); err == nil || !strings.Contains(err.Error(), "abgelehnt") {
		t.Fatalf("wrong secret: %v", err)
	}
	cfg = s.Config()
	cfg.Fingerprint = strings.Repeat("ab", 32)
	if _, err := opnsense.Test(context.Background(), s.Host(), cfg); err == nil || !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	s.Forbid("/api/ipsec/")
	r, err := opnsense.Test(context.Background(), s.Host(), s.Config())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "OPNsense 25.7.6-amd64" || r.Gateways != 2 || r.Tunnels != 2 || r.Services != 7 || len(r.Missing) != 1 || r.Missing[0] != "IPsec" {
		t.Fatalf("test result %+v", r)
	}
}
