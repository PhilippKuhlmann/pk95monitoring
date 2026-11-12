package nut_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/collect/nut"
	"pk95monitoring/internal/fake/fakenut"
	"pk95monitoring/internal/model"
)

func device(s *fakenut.Server, cfg model.NUTConfig) model.Device {
	cfg.Port = s.Port()
	return model.Device{ID: "d1", Name: "USV", Type: model.TypeNUT, Host: s.Host(), NUT: &cfg}
}

func metric(ck model.Check, name, inst string) (float64, bool) {
	for _, m := range ck.Metrics {
		if m.Name == name && m.Instance == inst {
			return m.Value, true
		}
	}
	return 0, false
}

func TestCollectUniFi(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"ups": fakenut.UniFiUPS()})
	defer s.Close()
	s.SetDesc("ups", "UniFi UPS Rack")

	checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("checks = %d", len(checks))
	}
	ck := checks[0]
	if ck.Key != "nut:ups" || ck.Name != "UniFi UPS Rack" || ck.Group != "USV" || ck.State != model.StateOK {
		t.Errorf("check = %+v", ck)
	}
	if !strings.HasPrefix(ck.Message, "Netzbetrieb, Akku lädt – Ladung 100 %, Restlaufzeit 41 min, Last 18 %") {
		t.Errorf("message = %q", ck.Message)
	}
	want := []struct {
		name, inst string
		v          float64
		unit       string
	}{
		{"ups.battery", "", 100, "%"},
		{"ups.runtime", "", 41, "min"},
		{"ups.load", "", 18, "%"},
		{"ups.input", "", 230.4, "V"},
		{"ups.output", "", 230, "V"},
		{"ups.power", "", 216, "W"},
		{"ups.battery_voltage", "", 27.2, "V"},
		{"temp", "USV", 33, "°C"},
		{"temp", "Batterie", 29.5, "°C"},
	}
	for _, w := range want {
		found := false
		for _, m := range ck.Metrics {
			if m.Name == w.name && m.Instance == w.inst {
				found = true
				if m.Value != w.v || m.Unit != w.unit {
					t.Errorf("%s:%s = %v %s, want %v %s", w.name, w.inst, m.Value, m.Unit, w.v, w.unit)
				}
			}
		}
		if !found {
			t.Errorf("metric %s:%s missing", w.name, w.inst)
		}
	}
	facts := map[string]string{
		"Hersteller": "Ubiquiti", "Modell": "UPS 2U", "Seriennummer": "F4E2C6A1B9D3", "Firmware": "1.3.7",
		"Akku-Datum": "2024/03/12", "Treiber": "usbhid-ups", "Status": "OL CHRG", "USV-Name": "ups",
	}
	for k, v := range facts {
		if ck.Facts[k] != v {
			t.Errorf("fact %s = %q, want %q", k, ck.Facts[k], v)
		}
	}
}

func TestCollectRealPower(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"ups": fakenut.UniFiUPS()})
	defer s.Close()
	s.SetVar("ups", "ups.realpower", "250")
	checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := metric(checks[0], "ups.power", ""); p != 250 {
		t.Errorf("ups.power = %v, want 250 (ups.realpower wins)", p)
	}
	if checks[0].Name != "USV ups" {
		t.Errorf("name = %q (no description)", checks[0].Name)
	}
}

func TestCollectMissingVars(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"bare": {"ups.status": "OL", "battery.charge": "notanumber"}})
	defer s.Close()
	checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	ck := checks[0]
	if ck.State != model.StateOK || ck.Message != "Netzbetrieb" || len(ck.Metrics) != 0 {
		t.Errorf("check = %+v", ck)
	}

	s.SetVar("bare", "ups.status", "")
	checks, _ = nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if checks[0].State != model.StateUnknown {
		t.Errorf("without ups.status: %+v", checks[0])
	}
}

func TestStatusFlags(t *testing.T) {
	tests := []struct {
		status, state, msg string
	}{
		{"OL", model.StateOK, "Netzbetrieb"},
		{"OL CHRG", model.StateOK, "Netzbetrieb, Akku lädt"},
		{"CHRG OL", model.StateOK, "Netzbetrieb, Akku lädt"},
		{"OB", model.StateWarn, "Läuft auf Batterie"},
		{"OB DISCHRG", model.StateWarn, "Läuft auf Batterie"},
		{"OB LB", model.StateCrit, "Läuft auf Batterie, Akku schwach"},
		{"OB LB FSD", model.StateCrit, "Läuft auf Batterie, Akku schwach, Wird heruntergefahren"},
		{"OL RB", model.StateWarn, "Netzbetrieb, Akku tauschen"},
		{"OL OVER", model.StateCrit, "Netzbetrieb, Überlast"},
		{"OL BYPASS", model.StateWarn, "Netzbetrieb, Bypass"},
		{"OFF", model.StateCrit, "Ausgeschaltet"},
		{"FSD", model.StateCrit, "Wird heruntergefahren"},
		{"OL TRIM", model.StateOK, "Netzbetrieb, Spannung wird abgesenkt"},
		{"OL BOOST", model.StateOK, "Netzbetrieb, Spannung wird angehoben"},
		{"OL CAL DISCHRG", model.StateOK, "Netzbetrieb, Akku entlädt, Kalibrierung läuft"},
		{"OL HB", model.StateOK, "Netzbetrieb, Akkuspannung hoch"},
		{"OL TEST", model.StateOK, "Netzbetrieb, Selbsttest läuft"},
		{"OL ECO", model.StateOK, "Netzbetrieb, Eco-Modus"},
		{"OL ALARM", model.StateWarn, "Netzbetrieb, Alarm: Replace battery!"},
		{"OL XYZ", model.StateOK, "Netzbetrieb, Status XYZ"},
		{"WAIT", model.StateUnknown, "Warte auf Daten vom Treiber"},
		{"ol chrg", model.StateOK, "Netzbetrieb, Akku lädt"},
		{"OB OVER RB BYPASS", model.StateCrit, "Läuft auf Batterie, Akku tauschen, Bypass, Überlast"},
	}
	for _, tt := range tests {
		vars := map[string]string{"ups.status": tt.status}
		if strings.Contains(tt.status, "ALARM") {
			vars["ups.alarm"] = "Replace battery!"
		}
		s := fakenut.New(map[string]map[string]string{"u": vars})
		checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
		s.Close()
		if err != nil {
			t.Fatalf("%s: %v", tt.status, err)
		}
		ck := checks[0]
		if ck.State != tt.state || ck.Message != tt.msg {
			t.Errorf("%q: %s %q, want %s %q", tt.status, ck.State, ck.Message, tt.state, tt.msg)
		}
		if ck.Facts["Status"] != tt.status {
			t.Errorf("%q: fact Status = %q", tt.status, ck.Facts["Status"])
		}
	}
}

func TestCollectMultipleUPS(t *testing.T) {
	b := fakenut.UniFiUPS()
	b["ups.status"] = "OB DISCHRG"
	b["battery.charge"] = "64"
	s := fakenut.New(map[string]map[string]string{"rack": fakenut.UniFiUPS(), "tower": b})
	defer s.Close()

	checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0].Key != "nut:rack" || checks[1].Key != "nut:tower" {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].State != model.StateOK || checks[1].State != model.StateWarn {
		t.Errorf("states = %s, %s", checks[0].State, checks[1].State)
	}
	// Several UPS in one device: the instance keeps their series apart.
	if v, ok := metric(checks[1], "ups.battery", "tower"); !ok || v != 64 {
		t.Errorf("tower ups.battery = %v %v", v, ok)
	}
	if _, ok := metric(checks[0], "temp", "rack/Batterie"); !ok {
		t.Errorf("rack temp instance missing: %+v", checks[0].Metrics)
	}

	// Only the configured UPS, without instance.
	checks, err = nut.New().Collect(context.Background(), device(s, model.NUTConfig{UPS: "tower"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Key != "nut:tower" {
		t.Fatalf("checks = %+v", checks)
	}
	if v, ok := metric(checks[0], "ups.battery", ""); !ok || v != 64 {
		t.Errorf("ups.battery = %v %v", v, ok)
	}

	// An unknown configured UPS is an unknown check, not an error.
	checks, err = nut.New().Collect(context.Background(), device(s, model.NUTConfig{UPS: "nope"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Key != "nut:nope" || checks[0].State != model.StateUnknown ||
		!strings.Contains(checks[0].Message, "vorhanden: rack, tower") {
		t.Errorf("checks = %+v", checks)
	}
}

func TestCollectNoUPS(t *testing.T) {
	s := fakenut.New(nil)
	defer s.Close()
	if _, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{})); err == nil ||
		!strings.Contains(err.Error(), "keine USV") {
		t.Errorf("error = %v", err)
	}
}

func TestCollectLogin(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"ups": fakenut.UniFiUPS()})
	defer s.Close()
	s.RequireLogin("mon user", `pa"ss\word`)

	// Without login, the listing is refused.
	_, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err == nil || !strings.Contains(err.Error(), "Zugriff verweigert") {
		t.Errorf("without login: %v", err)
	}
	// Wrong password.
	_, err = nut.New().Collect(context.Background(), device(s, model.NUTConfig{Username: "mon user", Password: "x"}))
	if err == nil || !strings.Contains(err.Error(), "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %v", err)
	}
	// Correct login (with quoting).
	checks, err := nut.New().Collect(context.Background(),
		device(s, model.NUTConfig{Username: "mon user", Password: `pa"ss\word`}))
	if err != nil || len(checks) != 1 || checks[0].State != model.StateOK {
		t.Errorf("login: %v %+v", err, checks)
	}
	// Line breaks never reach the server.
	_, err = nut.New().Collect(context.Background(),
		device(s, model.NUTConfig{Username: "a\nLIST UPS", Password: "x"}))
	if err == nil || !strings.Contains(err.Error(), "Zeilenumbruch") {
		t.Errorf("newline: %v", err)
	}
}

func TestCollectStale(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"a": fakenut.UniFiUPS(), "b": fakenut.UniFiUPS()})
	defer s.Close()
	s.SetError("a", "DATA-STALE")
	s.SetError("b", "DRIVER-NOT-CONNECTED")
	checks, err := nut.New().Collect(context.Background(), device(s, model.NUTConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	for _, ck := range checks {
		if ck.State != model.StateUnknown || ck.Message == "" || len(ck.Metrics) != 0 {
			t.Errorf("check = %+v", ck)
		}
	}
	if !strings.Contains(checks[0].Message, "DATA-STALE") || !strings.Contains(checks[1].Message, "Treiber nicht verbunden") {
		t.Errorf("messages = %q, %q", checks[0].Message, checks[1].Message)
	}
}

func TestCollectUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	d := model.Device{Host: "127.0.0.1", Type: model.TypeNUT, NUT: &model.NUTConfig{Port: port}}
	_, err = nut.New().Collect(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "Verbindung abgelehnt") {
		t.Errorf("error = %v", err)
	}

	// Server that accepts but never answers: timeout.
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	d.NUT.Port = ln.Addr().(*net.TCPAddr).Port
	c := &nut.Collector{Timeout: 150 * time.Millisecond}
	start := time.Now()
	_, err = c.Collect(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "Zeitüberschreitung") {
		t.Errorf("error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestTest(t *testing.T) {
	stale := fakenut.UniFiUPS()
	s := fakenut.New(map[string]map[string]string{"rack": fakenut.UniFiUPS(), "old": stale})
	defer s.Close()
	s.SetDesc("rack", "UniFi UPS 2U")
	s.SetError("old", "DATA-STALE")
	s.RequireLogin("monuser", "secret")
	ctx := context.Background()

	res, err := nut.Test(ctx, s.Host(), model.NUTConfig{Port: s.Port(), Username: "monuser", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Server != fakenut.Version || len(res.UPS) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.UPS[0].Name != "old" || !strings.Contains(res.UPS[0].Status, "DATA-STALE") {
		t.Errorf("old = %+v", res.UPS[0])
	}
	if r := res.UPS[1]; r.Name != "rack" || r.Desc != "UniFi UPS 2U" || r.Model != "UPS 2U" || r.Status != "Netzbetrieb, Akku lädt" {
		t.Errorf("rack = %+v", r)
	}

	res, err = nut.Test(ctx, s.Host(), model.NUTConfig{Port: s.Port(), UPS: "rack", Username: "monuser", Password: "secret"})
	if err != nil || len(res.UPS) != 1 || res.UPS[0].Name != "rack" {
		t.Errorf("single: %v %+v", err, res)
	}
	if _, err = nut.Test(ctx, s.Host(), model.NUTConfig{Port: s.Port(), UPS: "x", Username: "monuser", Password: "secret"}); err == nil ||
		!strings.Contains(err.Error(), "nicht bekannt") {
		t.Errorf("unknown ups: %v", err)
	}
	if _, err = nut.Test(ctx, s.Host(), model.NUTConfig{Port: s.Port(), Username: "monuser", Password: "wrong"}); err == nil ||
		!strings.Contains(err.Error(), "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %v", err)
	}
	if _, err = nut.Test(ctx, "", model.NUTConfig{}); err == nil {
		t.Error("empty host accepted")
	}
}

func TestDrift(t *testing.T) {
	s := fakenut.New(map[string]map[string]string{"ups": fakenut.UniFiUPS()})
	defer s.Close()
	now := time.Unix(1_700_000_000, 0)
	s.SetClock(func() time.Time { return now })
	s.EnableDrift()
	c := nut.New()
	d := device(s, model.NUTConfig{})
	seen := map[float64]bool{}
	for i := range 5 {
		now = now.Add(time.Duration(i) * 3 * time.Minute)
		checks, err := c.Collect(context.Background(), d)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := metric(checks[0], "ups.input", "")
		if v < 225 || v > 236 {
			t.Errorf("ups.input = %v", v)
		}
		seen[v] = true
	}
	if len(seen) < 2 {
		t.Errorf("values do not drift: %v", seen)
	}
}
