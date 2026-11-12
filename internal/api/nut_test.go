package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"pk95monitoring/internal/fake/fakenut"
	"pk95monitoring/internal/model"
	"pk95monitoring/web"
)

// TestNUTEndToEnd covers NUT devices over HTTP with the real core: the connection test
// (wrong and stored password), validation, password redaction, keeping the stored password
// on save, polling, and the SNMP profile "unifi".
func TestNUTEndToEnd(t *testing.T) {
	dir := t.TempDir()
	c := newTestCore(t, dir)
	os.WriteFile(SetupCodePath(dir), []byte("ABCD-EFGH\n"), 0o600)
	cert, _ := testCert(t)
	srv := httptest.NewUnstartedServer(New(c, web.FS, Options{Secure: true}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	hc := srv.Client()
	hc.Jar = jar
	ui := &client{t: t, base: srv.URL, hc: hc}
	ui.must("POST", "/api/setup", map[string]string{"code": "ABCD-EFGH", "username": "admin", "password": "geheim-geheim"}, nil)

	ups := fakenut.New(map[string]map[string]string{"ups": fakenut.UniFiUPS()})
	defer ups.Close()
	ups.SetDesc("ups", "UniFi UPS 2U")
	ups.RequireLogin("monuser", "s3cret-pw")

	type testOut struct {
		OK      bool   `json:"ok"`
		Server  string `json:"server"`
		Message string `json:"message"`
		UPS     []struct {
			Name   string `json:"name"`
			Desc   string `json:"desc"`
			Model  string `json:"model"`
			Status string `json:"status"`
		} `json:"ups"`
	}
	nutCfg := func(pass string) map[string]any {
		return map[string]any{"port": ups.Port(), "username": "monuser", "password": pass}
	}

	// Connection test: wrong password, then the right one.
	var res testOut
	ui.must("POST", "/api/test/nut", map[string]any{"host": ups.Host(), "nut": nutCfg("falsch")}, &res)
	if res.OK || !strings.Contains(res.Message, "Anmeldung abgelehnt") {
		t.Errorf("wrong password: %+v", res)
	}
	res = testOut{}
	ui.must("POST", "/api/test/nut", map[string]any{"host": ups.Host(), "nut": nutCfg("s3cret-pw")}, &res)
	if !res.OK || res.Server != fakenut.Version || len(res.UPS) != 1 || res.UPS[0].Name != "ups" ||
		res.UPS[0].Desc != "UniFi UPS 2U" || res.UPS[0].Status == "" {
		t.Errorf("test: %+v", res)
	}
	var fe struct {
		Fields map[string]string `json:"fields"`
	}
	if code := ui.do("POST", "/api/test/nut", map[string]any{"host": " ", "nut": nutCfg("x")}, &fe); code != 422 || fe.Fields["host"] == "" {
		t.Errorf("test without host: %d %v", code, fe.Fields)
	}

	// Validation.
	fe.Fields = nil
	if code := ui.do("POST", "/api/devices", map[string]any{"name": "USV", "type": "nut", "host": "", "enabled": true,
		"nut": map[string]any{"port": 70000, "ups": "ups@10.0.0.9", "username": "a b", "password": "x\ny"}}, &fe); code != 422 {
		t.Fatalf("invalid nut device: %d", code)
	}
	for _, k := range []string{"host", "nut.port", "nut.ups", "nut.username", "nut.password"} {
		if fe.Fields[k] == "" {
			t.Errorf("no field error %s: %v", k, fe.Fields)
		}
	}

	// Create: the password is never sent back.
	var raw json.RawMessage
	ui.must("POST", "/api/devices", map[string]any{"name": "USV Technik", "type": "nut", "host": ups.Host(), "enabled": true,
		"nut": map[string]any{"port": ups.Port(), "ups": "ups", "username": "monuser", "password": "s3cret-pw"}}, &raw)
	if strings.Contains(string(raw), "s3cret-pw") {
		t.Fatalf("password in answer: %s", raw)
	}
	var dev deviceOut
	json.Unmarshal(raw, &dev)
	if dev.NUT == nil || !dev.NUT.HasPassword || dev.NUT.Password != "" || dev.NUT.Username != "monuser" || dev.NUT.UPS != "ups" || dev.NUT.Port != ups.Port() {
		t.Fatalf("nut out %+v", dev.NUT)
	}

	// Save with an empty password keeps the stored one (the poll still logs in).
	ui.must("PUT", "/api/devices/"+dev.ID, map[string]any{"name": "USV Technik", "type": "nut", "host": ups.Host(), "enabled": true,
		"nut": map[string]any{"port": ups.Port(), "ups": "ups", "username": "monuser", "password": ""}}, nil)
	if d, err := c.Device(dev.ID); err != nil || d.NUT == nil || d.NUT.Password != "s3cret-pw" {
		t.Fatalf("stored password lost: %+v %v", d.NUT, err)
	}
	var polled struct {
		Status model.DeviceStatus `json:"status"`
	}
	ui.must("POST", "/api/devices/"+dev.ID+"/poll", nil, &polled)
	var check *model.CheckStatus
	for i, cs := range polled.Status.Checks {
		if cs.Key == "nut:ups" {
			check = &polled.Status.Checks[i]
		}
	}
	if check == nil || check.State != model.StateOK || check.Group != "USV" || check.Name != "UniFi UPS 2U" {
		t.Fatalf("poll: %+v (error %q)", polled.Status.Checks, polled.Status.Error)
	}
	battery := false
	for _, m := range check.Metrics {
		battery = battery || (m.Name == "ups.battery" && m.Value == 100)
	}
	if !battery {
		t.Errorf("no ups.battery: %+v", check.Metrics)
	}

	// The connection test of the stored device uses the stored password.
	res = testOut{}
	ui.must("POST", "/api/test/nut", map[string]any{"id": dev.ID, "host": ups.Host(), "nut": nutCfg("")}, &res)
	if !res.OK {
		t.Errorf("test with stored password: %+v", res)
	}
	raw = nil
	ui.must("GET", "/api/devices/"+dev.ID, nil, &raw)
	if strings.Contains(string(raw), "s3cret-pw") || !strings.Contains(string(raw), `"has_password":true`) {
		t.Errorf("device detail leaks or lacks has_password: %s", raw)
	}

	// The device list shows the NUT device with its state.
	var list struct {
		Devices []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"devices"`
	}
	ui.must("GET", "/api/devices", nil, &list)
	found := false
	for _, d := range list.Devices {
		found = found || (d.ID == dev.ID && d.Type == model.TypeNUT)
	}
	if !found {
		t.Errorf("nut device not listed: %+v", list.Devices)
	}

	// SNMP profile "unifi" is accepted.
	var sw deviceOut
	ui.must("POST", "/api/devices", map[string]any{"name": "UniFi-Switch", "type": "snmp", "host": "192.0.2.3", "enabled": false,
		"snmp": map[string]any{"version": "2c", "community": "public", "profile": "unifi"}}, &sw)
	if sw.SNMP == nil || sw.SNMP.Profile != "unifi" {
		t.Errorf("unifi profile: %+v", sw.SNMP)
	}
}
