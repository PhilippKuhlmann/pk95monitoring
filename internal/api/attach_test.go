package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/agent"
	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/web"
)

// TestAttachEndToEnd covers attached agents over HTTP with the real core: suggestions,
// attach/detach, hidden list entries, the parent state, registration with --device, the
// install commands for a device and the detach on parent delete.
func TestAttachEndToEnd(t *testing.T) {
	dir := t.TempDir()
	c := newTestCore(t, dir)
	os.WriteFile(SetupCodePath(dir), []byte("ABCD-EFGH\n"), 0o600)
	cert, leaf := testCert(t)
	sum := sha256.Sum256(leaf.Raw)
	fp := hex.EncodeToString(sum[:])
	srv := httptest.NewUnstartedServer(New(c, web.FS, Options{Secure: true, Fingerprint: strings.ToUpper(fp), PubKeyPin: agentdist.PinFromCert(leaf), Pinned: true}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	hc := srv.Client()
	hc.Jar = jar
	ui := &client{t: t, base: srv.URL, hc: hc}
	anon := &client{t: t, base: srv.URL, hc: srv.Client()}
	ui.must("POST", "/api/setup", map[string]string{"code": "ABCD-EFGH", "username": "admin", "password": "geheim-geheim"}, nil)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	var parent struct {
		ID string `json:"id"`
	}
	ui.must("POST", "/api/devices", map[string]any{"name": "Server", "type": "net", "host": "srv1.example.lan", "enabled": true,
		"checks": []map[string]any{{"type": "http", "target": target.URL}}}, &parent)
	ui.must("POST", "/api/devices/"+parent.ID+"/poll", nil, nil)

	var ag struct {
		EnrollKey string           `json:"enroll_key"`
		LinuxCmd  string           `json:"linux_cmd"`
		Agents    []core.DeviceRow `json:"agents"`
	}
	ui.must("GET", "/api/agents", nil, &ag)

	// A standalone agent whose host name matches the parent's host gets a suggestion.
	var regA model.AgentRegistered
	if code := anon.do("POST", "/agent/v1/register", model.AgentRegister{Key: ag.EnrollKey, AgentID: "machine-a", Hostname: "SRV1",
		OS: "linux", Platform: "debian", Version: "1.2.3", IPs: []string{"10.9.9.9"}}, &regA); code != 200 || regA.Token == "" {
		t.Fatalf("register A: %d %+v", code, regA)
	}
	type detailT struct {
		Device   deviceOut         `json:"device"`
		Suggest  []core.Suggestion `json:"suggest"`
		Parent   *parentOut        `json:"parent"`
		Attached []struct {
			Device deviceOut          `json:"device"`
			Status model.DeviceStatus `json:"status"`
		} `json:"attached"`
	}
	getDetail := func(id string) (d detailT) {
		ui.must("GET", "/api/devices/"+id, nil, &d)
		return d
	}
	detail := getDetail(regA.DeviceID)
	if len(detail.Suggest) != 1 || detail.Suggest[0].ID != parent.ID || detail.Suggest[0].Type != model.TypeNet || detail.Parent != nil {
		t.Fatalf("suggest %+v", detail)
	}

	// Validation errors.
	var fe struct {
		Fields map[string]string `json:"fields"`
	}
	if code := ui.do("POST", "/api/devices/"+parent.ID+"/attach", map[string]any{"agent_id": ""}, &fe); code != 422 || fe.Fields["agent_id"] == "" {
		t.Errorf("attach without agent: %d %v", code, fe)
	}
	if code := ui.do("POST", "/api/devices/"+parent.ID+"/attach", map[string]any{"agent_id": parent.ID}, &fe); code != 422 || fe.Fields["agent_id"] == "" {
		t.Errorf("attach a non-agent: %d %v", code, fe)
	}
	if code := ui.do("POST", "/api/devices/"+regA.DeviceID+"/attach", map[string]any{"agent_id": regA.DeviceID}, &fe); code != 422 || fe.Fields["agent_id"] == "" {
		t.Errorf("attach to an agent: %d %v", code, fe)
	}
	if code := ui.do("POST", "/api/devices/"+parent.ID+"/attach", map[string]any{"agent_id": regA.DeviceID, "node": "n\x01"}, &fe); code != 422 || fe.Fields["node"] == "" {
		t.Errorf("invalid node: %d %v", code, fe)
	}
	if code := ui.do("POST", "/api/devices/nope/attach", map[string]any{"agent_id": regA.DeviceID}, nil); code != 404 {
		t.Errorf("attach to unknown device: %d", code)
	}
	if code := ui.do("POST", "/api/devices/nope/detach", nil, nil); code != 404 {
		t.Errorf("detach unknown device: %d", code)
	}

	// Attach over the API.
	var att struct {
		Device deviceOut `json:"device"`
	}
	ui.must("POST", "/api/devices/"+parent.ID+"/attach", map[string]any{"agent_id": regA.DeviceID}, &att)
	if att.Device.ID != parent.ID {
		t.Errorf("attach answer %+v", att.Device)
	}
	var devs struct {
		Devices []core.DeviceRow `json:"devices"`
	}
	ui.must("GET", "/api/devices", nil, &devs)
	if len(devs.Devices) != 1 || devs.Devices[0].ID != parent.ID || devs.Devices[0].Attached != 1 {
		t.Fatalf("devices with attached agent %+v", devs.Devices)
	}
	var ov struct {
		Counts  core.Counts      `json:"counts"`
		Devices []core.DeviceRow `json:"devices"`
	}
	ui.must("GET", "/api/overview", nil, &ov)
	if len(ov.Devices) != 1 {
		t.Errorf("overview devices %+v", ov.Devices)
	}
	ui.must("GET", "/api/agents", nil, &ag)
	if len(ag.Agents) != 1 || ag.Agents[0].ParentID != parent.ID || ag.Agents[0].ParentName != "Server" {
		t.Errorf("agents %+v", ag.Agents)
	}
	detail = getDetail(regA.DeviceID)
	if detail.Parent == nil || detail.Parent.ID != parent.ID || detail.Parent.Name != "Server" || detail.Parent.Type != model.TypeNet || detail.Suggest != nil {
		t.Errorf("agent detail %+v", detail)
	}
	detail = getDetail(parent.ID)
	if len(detail.Attached) != 1 || detail.Attached[0].Device.ID != regA.DeviceID || detail.Attached[0].Device.ParentID != parent.ID {
		t.Errorf("parent detail %+v", detail.Attached)
	}

	// A critical value of the agent makes the parent critical.
	ac, err := agent.NewClient(srv.URL, regA.Token, fp, false)
	if err != nil {
		t.Fatal(err)
	}
	disk := model.Check{Key: "disk:/", Name: "Festplatte /", Group: "Festplatten", State: model.StateOK,
		Metrics: []model.Metric{{Name: "disk.usage", Instance: "/", Value: 97, Unit: "%"}}}
	if _, err := ac.Report(context.Background(), model.AgentReport{AgentID: "machine-a", Hostname: "SRV1", OS: "linux",
		Version: "1.2.3", Time: time.Now().Unix(), Checks: []model.Check{disk}}); err != nil {
		t.Fatal(err)
	}
	ui.must("GET", "/api/devices", nil, &devs)
	if r := devs.Devices[0]; r.State != model.StateCrit || !strings.HasPrefix(r.Message, "SRV1: Festplatte /") || r.Checks.Crit != 1 {
		t.Errorf("parent row %s %q %+v", r.State, r.Message, r.Checks)
	}
	ui.must("GET", "/api/overview", nil, &ov)
	if ov.Counts.Crit != 1 || ov.Counts.OK != 0 {
		t.Errorf("overview counts %+v", ov.Counts)
	}
	var alerts struct {
		Alerts []model.Alert `json:"alerts"`
	}
	ui.must("GET", "/api/alerts?state=active&device="+regA.DeviceID, nil, &alerts)
	if len(alerts.Alerts) != 1 || alerts.Alerts[0].DeviceName != "Server / SRV1" {
		t.Errorf("agent alert %+v", alerts.Alerts)
	}

	// Detach over the API.
	var det struct {
		Device deviceOut `json:"device"`
	}
	ui.must("POST", "/api/devices/"+regA.DeviceID+"/detach", nil, &det)
	if det.Device.ID != regA.DeviceID || det.Device.ParentID != "" {
		t.Errorf("detach answer %+v", det.Device)
	}
	ui.must("GET", "/api/devices", nil, &devs)
	if len(devs.Devices) != 2 {
		t.Errorf("devices after detach %+v", devs.Devices)
	}

	// Install commands for the device.
	ui.must("GET", "/api/agents?device="+parent.ID+"&node=n1", nil, &ag)
	if !strings.Contains(ag.LinuxCmd, " --device '"+parent.ID+"' --node 'n1'") {
		t.Errorf("linux command %q", ag.LinuxCmd)
	}
	if code := ui.do("GET", "/api/agents?device=nope", nil, nil); code != 404 {
		t.Errorf("commands for unknown device: %d", code)
	}
	if code := ui.do("GET", "/api/agents?device="+regA.DeviceID, nil, nil); code != 400 {
		t.Errorf("commands for an agent: %d", code)
	}

	// Registration with --device.
	var e struct {
		Error string `json:"error"`
	}
	if code := anon.do("POST", "/agent/v1/register", model.AgentRegister{Key: ag.EnrollKey, AgentID: "machine-x", Hostname: "x",
		DeviceID: "nope"}, &e); code != 400 || e.Error == "" {
		t.Errorf("register with unknown device: %d %+v", code, e)
	}
	if code := anon.do("POST", "/agent/v1/register", model.AgentRegister{Key: ag.EnrollKey, AgentID: "machine-x", Hostname: "x",
		DeviceID: regA.DeviceID}, &e); code != 400 || e.Error == "" {
		t.Errorf("register with an agent as device: %d %+v", code, e)
	}
	regB, err := agent.RegisterDevice(context.Background(), srv.URL, ag.EnrollKey, fp, false, parent.ID, "n2")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Device(regB.DeviceID); d.ParentID != parent.ID || d.Node != "n2" {
		t.Errorf("registered with --device: %+v", d)
	}
	ui.must("GET", "/api/devices", nil, &devs)
	if len(devs.Devices) != 2 {
		t.Errorf("devices after register with --device %+v", devs.Devices)
	}

	// Deleting the parent detaches its agents.
	ui.must("DELETE", "/api/devices/"+parent.ID, nil, nil)
	ui.must("GET", "/api/devices", nil, &devs)
	if len(devs.Devices) != 2 {
		t.Errorf("devices after parent delete %+v", devs.Devices)
	}
	detail = getDetail(regB.DeviceID)
	if detail.Parent != nil || detail.Device.ParentID != "" || detail.Suggest == nil {
		t.Errorf("agent after parent delete %+v", detail)
	}
}
