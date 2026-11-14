package unifi

import (
	"context"
	"strings"
	"testing"

	"pk95monitoring/internal/fake/fakeunifi"
	"pk95monitoring/internal/model"
)

func byKey(cs []model.Check) map[string]model.Check {
	m := map[string]model.Check{}
	for _, c := range cs {
		m[c.Key] = c
	}
	return m
}

func setup(t *testing.T) (*Collector, *fakeunifi.Server, model.Device) {
	t.Helper()
	srv := fakeunifi.New()
	t.Cleanup(srv.Close)
	cfg := srv.Config()
	return New(), srv, model.Device{ID: "u1", Type: model.TypeUniFi, Host: srv.Host(), UniFi: &cfg}
}

func TestCollect(t *testing.T) {
	c, srv, d := setup(t)
	cs, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(cs)
	if k := m["unifi:console"]; k.State != model.StateOK || k.Message != "Dream Machine Pro · Network 9.0.114 · Protect 5.2.46" ||
		k.Facts["Firmware-Update"] != "4.2.9" {
		t.Errorf("console %+v", k)
	}
	if w := m["unifi:wan:default"]; w.State != model.StateOK || w.Message != "Verbunden · 11 ms · WAN-IP 87.123.45.67" || w.Facts["Anbieter"] != "Deutsche Telekom AG" {
		t.Errorf("wan %+v", w)
	}
	if ap := m["unifi:dev:74acb9112203"]; ap.State != model.StateOK || ap.Message != "Verbunden · 14 Clients · Access Point U6PRO" {
		t.Errorf("ap %+v", ap)
	}
	if ap := m["unifi:dev:74acb9112204"]; ap.State != model.StateCrit || !strings.HasPrefix(ap.Message, "Getrennt seit ") {
		t.Errorf("offline ap %+v", ap)
	}
	if ap := m["unifi:dev:74acb9112205"]; !strings.HasSuffix(ap.Message, "Update auf 6.6.77.15402 verfügbar") {
		t.Errorf("update %+v", ap)
	}
	if _, ok := m["unifi:dev:74acb9112206"]; ok {
		t.Error("device not adopted listed")
	}
	if n := m["unifi:nvr"]; n.State != model.StateOK || n.Message != "Speicher 89 % von 7,8 TB" || n.Facts["Speicher"] == "" {
		t.Errorf("nvr %+v", n)
	}
	if dk := m["unifi:disk:1"]; dk.State != model.StateOK || dk.Facts["Seriennummer"] != "WD-CA0K3X1Y" {
		t.Errorf("disk %+v", dk)
	}
	if cm := m["unifi:cam:cam0000000000000000000001"]; cm.State != model.StateOK || cm.Message != "Verbunden · nimmt auf (Dauerhaft) · G4 Bullet" {
		t.Errorf("camera %+v", cm)
	}
	if cm := m["unifi:cam:cam0000000000000000000003"]; cm.State != model.StateCrit || !strings.HasPrefix(cm.Message, "Getrennt seit ") {
		t.Errorf("offline camera %+v", cm)
	}
	if cm := m["unifi:cam:cam0000000000000000000004"]; cm.State != model.StateWarn || !strings.HasPrefix(cm.Message, "Nimmt nicht auf · verbunden") {
		t.Errorf("not recording %+v", cm)
	}
	if _, ok := m["unifi:cam:cam0000000000000000000009"]; ok {
		t.Error("camera not adopted listed")
	}

	// the session is reused; an expired one logs in again
	if _, err := c.Collect(context.Background(), d); err != nil || srv.Logins() != 1 {
		t.Fatalf("second poll: logins %d, %v", srv.Logins(), err)
	}
	srv.Expire()
	srv.SetDiskHealthy(false)
	srv.SetProtect(true)
	m = byKey(mustCollect(t, c, d))
	if srv.Logins() != 2 {
		t.Errorf("logins after expiry %d", srv.Logins())
	}
	if dk := m["unifi:disk:1"]; dk.State != model.StateCrit || !strings.Contains(dk.Message, "Festplatte tauschen") {
		t.Errorf("bad disk %+v", dk)
	}
	// without Protect only Network
	srv.SetProtect(false)
	m = byKey(mustCollect(t, c, d))
	if _, ok := m["unifi:nvr"]; ok || m["unifi:console"].Message != "Dream Machine Pro · Network 9.0.114" {
		t.Errorf("without protect %+v", m["unifi:console"])
	}
}

func mustCollect(t *testing.T, c *Collector, d model.Device) []model.Check {
	t.Helper()
	cs, err := c.Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestLoginAndTest(t *testing.T) {
	c, srv, d := setup(t)
	bad := *d.UniFi
	bad.Password = "falsch"
	d.UniFi = &bad
	if _, err := c.Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %v", err)
	}
	pinned := srv.Config()
	pinned.Fingerprint = strings.Repeat("ab", 32)
	if _, err := Test(context.Background(), srv.Host(), pinned); err == nil || !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Errorf("wrong fingerprint: %v", err)
	}
	res, err := Test(context.Background(), srv.Host(), srv.Config())
	if err != nil || res.Console != "Dream Machine Pro" || res.Network != "9.0.114" || res.Protect != "5.2.46" || res.Devices != 5 || res.Cameras != 5 {
		t.Errorf("test %+v %v", res, err)
	}
}

func TestWAN(t *testing.T) {
	lat := 30.0
	hs := []health{{Subsystem: "wan", Status: "ok"}, {Subsystem: "www", Status: "error", Latency: &lat}}
	gw := []netDevice{{Type: "udm", Wan1: &wan{Up: true, IP: "1.2.3.4"}, Wan2: &wan{Up: false}}}
	ch, ok := wanCheck("default", "", hs, gw)
	if !ok || ch.State != model.StateCrit || !strings.HasPrefix(ch.Message, "Kein Internet") || ch.Facts["WAN 2"] != "getrennt" {
		t.Errorf("no internet %+v", ch)
	}
	hs[1].Status = "ok"
	if ch, _ := wanCheck("default", "Filiale", hs, gw); ch.State != model.StateWarn || !strings.Contains(ch.Message, "WAN 2 getrennt") || ch.Name != "Internet (Filiale)" {
		t.Errorf("failover line down %+v", ch)
	}
	if _, ok := wanCheck("default", "", []health{{Subsystem: "wan", Status: "unknown"}}, nil); ok {
		t.Error("unknown WAN reported")
	}
}
