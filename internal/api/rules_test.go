package api

import (
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/web"
)

func TestRedactDeviceRules(t *testing.T) {
	m := toMap(t, redactDevice(model.Device{ID: "d", Type: model.TypeNet}))
	if r, ok := m["rules"].([]any); !ok || len(r) != 0 {
		t.Fatalf("nil rules must be []: %v", m["rules"])
	}
	m = toMap(t, redactDevice(model.Device{ID: "d", Type: model.TypeNet,
		Rules: []model.CheckRule{{Match: "snmp:if:*", Mode: model.ModeQuiet}}}))
	r := m["rules"].([]any)
	if len(r) != 1 || r[0].(map[string]any)["match"] != "snmp:if:*" || r[0].(map[string]any)["mode"] != "quiet" {
		t.Fatalf("rules %v", r)
	}
}

// TestRulesRoundTrip saves rules over the API and reads them back together with the status
// fields they produce (checks[].mode/muted, hidden).
func TestRulesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := newTestCore(t, dir)
	os.WriteFile(SetupCodePath(dir), []byte("ABCD-EFGH\n"), 0o600)
	srv := httptest.NewServer(New(c, web.FS, Options{}))
	defer srv.Close()
	hc := srv.Client()
	hc.Jar, _ = cookiejar.New(nil)
	ui := &client{t: t, base: srv.URL, hc: hc}
	ui.must("POST", "/api/setup", map[string]string{"code": "abcd-efgh", "username": "admin", "password": "geheim-geheim"}, nil)

	// New device: rules round-trip; none given → [].
	var created map[string]any
	ui.must("POST", "/api/devices", map[string]any{"name": "Web", "type": "net", "host": "127.0.0.1", "enabled": true}, &created)
	if r, ok := created["rules"].([]any); !ok || len(r) != 0 {
		t.Fatalf("created rules %v", created["rules"])
	}
	id := created["id"].(string)
	created["rules"] = []map[string]any{{"match": " net:* ", "mode": "quiet"}, {"match": "net:abc", "mode": "off"}}
	var saved model.Device
	ui.must("PUT", "/api/devices/"+id, created, &saved)
	want := []model.CheckRule{{Match: "net:*", Mode: model.ModeQuiet}, {Match: "net:abc", Mode: model.ModeOff}}
	if len(saved.Rules) != 2 || saved.Rules[0] != want[0] || saved.Rules[1] != want[1] {
		t.Fatalf("saved rules %+v", saved.Rules)
	}
	var detail struct {
		Device model.Device `json:"device"`
	}
	ui.must("GET", "/api/devices/"+id, nil, &detail)
	if len(detail.Device.Rules) != 2 || detail.Device.Rules[1] != want[1] {
		t.Fatalf("GET rules %+v", detail.Device.Rules)
	}

	// Validation errors are keyed rules.N.field.
	var fe struct {
		Fields map[string]string `json:"fields"`
	}
	created["rules"] = []map[string]any{{"match": "reach", "mode": "off"}, {"match": "", "mode": "x"}}
	if code := ui.do("PUT", "/api/devices/"+id, created, &fe); code != 422 ||
		fe.Fields["rules.0.match"] == "" || fe.Fields["rules.1.match"] == "" || fe.Fields["rules.1.mode"] == "" {
		t.Fatalf("validation: %d %v", code, fe.Fields)
	}

	// An agent with a quiet and an off rule: the status carries mode, muted and hidden.
	agentID := "ag1"
	if err := c.ImportDevice(model.Device{ID: agentID, Name: "db01", Type: model.TypeAgent, Host: "10.0.0.22", Enabled: true,
		Agent: &model.AgentConfig{AgentID: "m-ag1", OS: "linux", Arch: "amd64"},
		Rules: []model.CheckRule{{Match: "updates", Mode: model.ModeQuiet}, {Match: "disk:/boot*", Mode: model.ModeOff}}}); err != nil {
		t.Fatal(err)
	}
	disk := func(mount string) model.Check {
		return model.Check{Key: "disk:" + mount, Name: "Festplatte " + mount, Group: "Festplatten", State: model.StateOK,
			Metrics: []model.Metric{{Name: "disk.usage", Instance: mount, Value: 30, Unit: "%"}}}
	}
	if _, err := c.IngestReport(agentID, model.AgentReport{Time: time.Now().Unix(), Checks: []model.Check{
		{Key: "updates", Name: "Updates", Group: "Updates", State: model.StateWarn, Message: "Neustart erforderlich"},
		disk("/"), disk("/boot")}}); err != nil {
		t.Fatal(err)
	}
	var ad struct {
		Device model.Device       `json:"device"`
		Status model.DeviceStatus `json:"status"`
	}
	ui.must("GET", "/api/devices/"+agentID, nil, &ad)
	var upd model.CheckStatus
	for _, cs := range ad.Status.Checks {
		if cs.Key == "updates" {
			upd = cs
		}
	}
	if upd.Mode != "quiet" || upd.Muted != "warn" || upd.State != "ok" || ad.Status.State != "ok" {
		t.Fatalf("quiet check over the API: %+v (device %s)", upd, ad.Status.State)
	}
	if len(ad.Status.Hidden) != 1 || ad.Status.Hidden[0] != (model.HiddenCheck{Key: "disk:/boot", Name: "Festplatte /boot", Group: "Festplatten"}) {
		t.Fatalf("hidden over the API: %+v", ad.Status.Hidden)
	}
	if len(ad.Device.Rules) != 2 {
		t.Fatalf("agent rules: %+v", ad.Device.Rules)
	}

	// Removing the off rule over the API drops the entry from hidden at once.
	var raw map[string]any
	ui.must("GET", "/api/devices/"+agentID, nil, &raw)
	dev := raw["device"].(map[string]any)
	dev["rules"] = []map[string]any{{"match": "updates", "mode": "quiet"}}
	ui.must("PUT", "/api/devices/"+agentID, dev, nil)
	ui.must("GET", "/api/devices/"+agentID, nil, &raw)
	if _, ok := raw["status"].(map[string]any)["hidden"]; ok {
		t.Fatalf("hidden still listed: %v", raw["status"])
	}
}
