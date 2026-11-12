package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/agent"
	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
	"pk95monitoring/web"
)

// These tests run the whole HTTP API against a real core on a temporary store. They are
// skipped while internal/core still has the placeholder bodies of contract.go.

func newTestCore(t *testing.T, dir string) (c *core.Core) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if strings.Contains(fmt.Sprint(r), "not implemented") {
				t.Skipf("internal/core is not implemented yet: %v", r)
			}
			panic(r)
		}
	}()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	agents := filepath.Join(dir, "agents")
	os.MkdirAll(agents, 0o755)
	os.WriteFile(filepath.Join(agents, agentdist.FileName("linux", "amd64")), []byte("linux agent"), 0o755)
	c, err = core.New(st, core.Options{Version: "1.2.3", DataDir: dir, AgentDir: agents, Instance: "Testkunde"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Agents == nil {
		c.Agents = agentdist.NewCatalog(agents, "1.2.3")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("core.Run did not stop")
		}
	})
	return c
}

// testCert is a self-signed certificate for 127.0.0.1, like the one internal/app creates.
func testCert(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pk95monitoring"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

// do sends a request with X-PK95 (like web/app.js) and decodes the JSON answer into out.
func (c *client) do(method, path string, body, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("X-PK95", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func (c *client) must(method, path string, body, out any) {
	c.t.Helper()
	var raw json.RawMessage
	if code := c.do(method, path, body, &raw); code != 200 {
		c.t.Fatalf("%s %s: HTTP %d %s", method, path, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

func TestEndToEnd(t *testing.T) {
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

	// ---- setup and session ----
	var sess struct {
		Setup    bool   `json:"setup"`
		Version  string `json:"version"`
		User     string `json:"user"`
		Instance string `json:"instance"`
	}
	ui.must("GET", "/api/session", nil, &sess)
	if !sess.Setup || sess.Version != "1.2.3" || sess.Instance != "Testkunde" || sess.User != "" {
		t.Fatalf("session %+v", sess)
	}
	if code := ui.do("GET", "/api/overview", nil, nil); code != 401 {
		t.Errorf("overview before login: %d", code)
	}
	var fe struct {
		Fields map[string]string `json:"fields"`
	}
	if code := ui.do("POST", "/api/setup", map[string]string{"code": "WRONG", "username": "admin", "password": "geheim-geheim"}, &fe); code != 422 || fe.Fields["code"] == "" {
		t.Errorf("wrong setup code: %d %v", code, fe)
	}
	if code := ui.do("POST", "/api/setup", map[string]string{"code": "abcd-efgh", "username": "admin", "password": "kurz"}, &fe); code != 422 || fe.Fields["password"] == "" {
		t.Errorf("short password: %d %v", code, fe)
	}
	ui.must("POST", "/api/setup", map[string]string{"code": "abcd-efgh", "username": "admin", "password": "geheim-geheim"}, nil)
	if _, err := os.Stat(SetupCodePath(dir)); !os.IsNotExist(err) {
		t.Error("setup code not removed")
	}
	if code := ui.do("POST", "/api/setup", map[string]string{"code": "abcd-efgh", "username": "x", "password": "geheim-geheim"}, nil); code != 409 {
		t.Errorf("second setup: %d", code)
	}
	ui.must("GET", "/api/session", nil, &sess)
	if sess.Setup || sess.User != "admin" {
		t.Errorf("session after setup %+v", sess)
	}
	ui.must("POST", "/api/logout", nil, nil)
	if code := ui.do("GET", "/api/overview", nil, nil); code != 401 {
		t.Errorf("overview after logout: %d", code)
	}
	if code := ui.do("POST", "/api/login", map[string]string{"username": "admin", "password": "falsch-falsch"}, nil); code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	ui.must("POST", "/api/login", map[string]string{"username": "admin", "password": "geheim-geheim"}, nil)

	// CSRF: a mutating request without X-PK95 is refused.
	req, _ := http.NewRequest("POST", srv.URL+"/api/agents/key", nil)
	if resp, err := hc.Do(req); err != nil || resp.StatusCode != 403 {
		t.Errorf("POST without X-PK95: %v %v", resp.StatusCode, err)
	}

	// ---- devices ----
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer target.Close()
	var netDev struct {
		ID     string           `json:"id"`
		Checks []model.NetCheck `json:"checks"`
	}
	ui.must("POST", "/api/devices", map[string]any{"name": "Webserver", "type": "net", "host": "127.0.0.1", "group": "Netzwerk", "enabled": true,
		"checks": []map[string]any{{"type": "http", "target": target.URL}}}, &netDev)
	if netDev.ID == "" || len(netDev.Checks) != 1 || netDev.Checks[0].ID == "" {
		t.Fatalf("net device %+v", netDev)
	}
	if code := ui.do("POST", "/api/devices", map[string]any{"type": "net", "enabled": true}, &fe); code != 422 || len(fe.Fields) == 0 {
		t.Errorf("invalid device: %d %v", code, fe)
	}

	var pxDev map[string]any
	ui.must("POST", "/api/devices", map[string]any{"name": "PVE", "type": "proxmox", "host": "127.0.0.1", "enabled": true,
		"proxmox": map[string]any{"port": 1, "token_id": "root@pam!mon", "secret": "s3cret-value"}}, &pxDev)
	pxID, _ := pxDev["id"].(string)
	var detail map[string]any
	ui.must("GET", "/api/devices/"+pxID, nil, &detail)
	px := detail["device"].(map[string]any)["proxmox"].(map[string]any)
	if px["secret"] != "" || px["has_secret"] != true || px["token_id"] != "root@pam!mon" {
		t.Errorf("proxmox not redacted: %v", px)
	}
	if _, ok := detail["status"]; !ok {
		t.Error("device detail without status")
	}
	// Saving with an empty secret keeps it.
	dev := detail["device"].(map[string]any)
	dev["name"] = "PVE-Cluster"
	ui.must("PUT", "/api/devices/"+pxID, dev, nil)
	if d, err := c.Device(pxID); err != nil || d.Proxmox == nil || d.Proxmox.Secret != "s3cret-value" || d.Name != "PVE-Cluster" {
		t.Errorf("secret lost on save: %+v %v", d.Proxmox, err)
	}
	if code := ui.do("PUT", "/api/devices/nope", dev, nil); code != 404 {
		t.Errorf("PUT unknown device: %d", code)
	}
	ui.must("POST", "/api/devices/"+netDev.ID+"/poll", nil, nil)
	var maint map[string]any
	ui.must("POST", "/api/devices/"+netDev.ID+"/maintenance", map[string]any{"until": time.Now().Add(time.Hour).Unix(), "note": "Update"}, &maint)
	if m, _ := maint["maintenance"].(map[string]any); m == nil || m["note"] != "Update" {
		t.Errorf("maintenance %v", maint)
	}
	if code := ui.do("POST", "/api/devices/"+netDev.ID+"/maintenance", map[string]any{"until": 1}, nil); code != 422 {
		t.Errorf("maintenance in the past: %d", code)
	}
	ui.must("POST", "/api/devices/"+netDev.ID+"/maintenance", map[string]any{"until": 0}, nil)

	var ov struct {
		Counts  map[string]int    `json:"counts"`
		Devices []core.DeviceRow  `json:"devices"`
		Alerts  []json.RawMessage `json:"alerts"`
		Groups  []string          `json:"groups"`
	}
	ui.must("GET", "/api/overview", nil, &ov)
	if len(ov.Devices) != 2 || ov.Counts == nil {
		t.Errorf("overview %+v", ov)
	}
	var devs struct {
		Devices []core.DeviceRow `json:"devices"`
		Groups  []string         `json:"groups"`
	}
	ui.must("GET", "/api/devices", nil, &devs)
	if len(devs.Devices) != 2 || len(devs.Groups) != 1 || devs.Groups[0] != "Netzwerk" {
		t.Errorf("devices %+v", devs)
	}

	// ---- agents ----
	var ag struct {
		EnrollKey   string             `json:"enroll_key"`
		ServerURL   string             `json:"server_url"`
		Fingerprint string             `json:"fingerprint"`
		LinuxCmd    string             `json:"linux_cmd"`
		WindowsCmd  string             `json:"windows_cmd"`
		Binaries    []agentdist.Binary `json:"binaries"`
		Agents      []core.DeviceRow   `json:"agents"`
	}
	ui.must("GET", "/api/agents", nil, &ag)
	if ag.EnrollKey == "" || ag.ServerURL != srv.URL || ag.Fingerprint == "" || len(ag.Binaries) != 3 || ag.Agents == nil {
		t.Fatalf("agents %+v", ag)
	}
	if !strings.Contains(ag.LinuxCmd, "--pinnedpubkey") || !strings.Contains(ag.LinuxCmd, ag.EnrollKey) || ag.WindowsCmd != "" {
		t.Errorf("commands %q %q", ag.LinuxCmd, ag.WindowsCmd)
	}
	resp, err := anon.hc.Get(srv.URL + "/agent/install.sh")
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !bytes.Contains(b, []byte(srv.URL)) {
			t.Errorf("install.sh: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	resp, err = anon.hc.Get(srv.URL + "/agent/download/linux/amd64")
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != "linux agent" || resp.Header.Get("Content-Type") != "application/octet-stream" ||
			!strings.Contains(resp.Header.Get("Content-Disposition"), "pk95agent-linux-amd64") {
			t.Errorf("download: %d %v", resp.StatusCode, resp.Header)
		}
	}
	if code := anon.do("GET", "/agent/download/windows/amd64", nil, nil); code != 404 {
		t.Errorf("missing binary: %d", code)
	}

	ctx := context.Background()
	if _, err := agent.Register(ctx, srv.URL, "wrong-key", fp, false); err == nil {
		t.Error("wrong enrollment key accepted")
	}
	reg, err := agent.Register(ctx, srv.URL, ag.EnrollKey, fp, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	ac, err := agent.NewClient(srv.URL, reg.Token, fp, false)
	if err != nil {
		t.Fatal(err)
	}
	cpu := model.Check{Key: "cpu", Name: "CPU", Group: "System", State: model.StateOK,
		Metrics: []model.Metric{{Name: "cpu.usage", Value: 12.5, Unit: "%"}}}
	rep := model.AgentReport{AgentID: agent.MachineID(), Hostname: "testhost", OS: "linux", Platform: "debian", Arch: "amd64",
		Version: "1.2.3", Time: time.Now().Unix(), Checks: []model.Check{cpu}}
	reply, err := ac.Report(ctx, rep)
	if err != nil || reply.Interval <= 0 {
		t.Fatalf("report: %+v %v", reply, err)
	}
	bad, _ := agent.NewClient(srv.URL, "wrong-token", fp, false)
	if _, err := bad.Report(ctx, rep); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong agent token: %v", err)
	}

	var series struct {
		Series []store.Series `json:"series"`
	}
	ui.must("GET", "/api/devices/"+reg.DeviceID+"/series", nil, &series)
	var cpuID int64
	for _, s := range series.Series {
		if s.Name == "cpu.usage" {
			cpuID = s.ID
		}
	}
	if cpuID == 0 {
		t.Fatalf("no cpu.usage series: %+v", series)
	}
	var met struct {
		From, To, Step int64
		Series         []seriesPoints `json:"series"`
	}
	ui.must("GET", fmt.Sprintf("/api/devices/%s/metrics?series=%d&range=1h", reg.DeviceID, cpuID), nil, &met)
	if met.Step <= 0 || met.To-met.From != 3600 || len(met.Series) != 1 || len(met.Series[0].Points) == 0 || met.Series[0].Points[0].Avg != 12.5 {
		t.Errorf("metrics %+v", met)
	}
	if code := ui.do("GET", fmt.Sprintf("/api/devices/%s/metrics?series=%d&range=2h", reg.DeviceID, cpuID), nil, nil); code != 400 {
		t.Errorf("bad range: %d", code)
	}
	// A series of another device is not returned.
	ui.must("GET", fmt.Sprintf("/api/devices/%s/metrics?series=%d&range=1h", netDev.ID, cpuID), nil, &met)
	if len(met.Series) != 0 {
		t.Errorf("foreign series returned: %+v", met.Series)
	}
	ui.must("GET", "/api/agents", nil, &ag)
	if len(ag.Agents) != 1 || ag.Agents[0].ID != reg.DeviceID {
		t.Errorf("registered agents %+v", ag.Agents)
	}

	// ---- alerts and events ----
	var al struct {
		Alerts []model.Alert `json:"alerts"`
		More   *bool         `json:"more"`
	}
	ui.must("GET", "/api/alerts?state=all&limit=50", nil, &al)
	if al.Alerts == nil || al.More == nil {
		t.Errorf("alerts %+v", al)
	}
	var ev struct {
		Events []store.Event `json:"events"`
		More   *bool         `json:"more"`
	}
	ui.must("GET", "/api/events?severity=&before=0", nil, &ev)
	if len(ev.Events) == 0 || ev.More == nil {
		t.Errorf("events %+v", ev)
	}

	// ---- settings ----
	var set map[string]any
	ui.must("GET", "/api/settings", nil, &set)
	if d, _ := set["defaults"].([]any); len(d) == 0 {
		t.Errorf("settings defaults %v", set["defaults"])
	}
	ui.must("PUT", "/api/settings", map[string]any{"thresholds": []map[string]any{{"metric": "cpu.usage", "warn": 70, "crit": 90}}}, &set)
	if th, _ := set["thresholds"].([]any); len(th) != 1 {
		t.Errorf("thresholds after save %v", set["thresholds"])
	}
	ui.must("PUT", "/api/settings", map[string]any{"notify": map[string]any{"min_severity": "warning", "hook_enabled": true, "hook_kind": "json",
		"hook_url": "https://hook.example/x", "hook_token": "hook-secret"}}, &set)
	n := set["notify"].(map[string]any)
	if n["hook_token"] != "" || n["has_hook_token"] != true || n["hook_url"] != "https://hook.example/x" {
		t.Errorf("notify %v", n)
	}
	if th, _ := set["thresholds"].([]any); len(th) != 1 {
		t.Errorf("notify save changed thresholds: %v", set["thresholds"])
	}
	ui.must("PUT", "/api/settings", map[string]any{"notify": map[string]any{"hook_token": ""}}, nil)
	if c.Settings().Notify.HookToken != "hook-secret" {
		t.Error("empty secret did not keep the stored one")
	}
	ui.must("PUT", "/api/settings", map[string]any{"thresholds": []any{}}, &set)
	if th, _ := set["thresholds"].([]any); len(th) != 0 {
		t.Errorf("thresholds not reset: %v", set["thresholds"])
	}
	ui.must("PUT", "/api/settings", map[string]any{"central": map[string]any{"enabled": false, "url": "", "token": "central-secret", "interval": 5}}, &set)
	if cen := set["central"].(map[string]any); cen["has_token"] != true || cen["token"] != nil {
		t.Errorf("central %v", cen)
	}
	b, _ := json.Marshal(set)
	if bytes.Contains(b, []byte("secret")) {
		t.Errorf("secret in settings: %s", b)
	}

	// ---- API tokens and external API ----
	var tok struct {
		Token  string `json:"token"`
		Name   string `json:"name"`
		ID     int64  `json:"id"`
		Prefix string `json:"prefix"`
	}
	ui.must("POST", "/api/tokens", map[string]string{"name": "Zentrale"}, &tok)
	if !strings.HasPrefix(tok.Token, store.APITokenPrefix) || tok.Name != "Zentrale" || tok.ID == 0 {
		t.Fatalf("token %+v", tok)
	}
	if code := ui.do("POST", "/api/tokens", map[string]string{"name": " "}, nil); code != 422 {
		t.Errorf("token without name: %d", code)
	}
	ext := func(path, token string, out any) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := anon.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var sum1 model.Summary
	if code := ext("/api/v1/summary", tok.Token, &sum1); code != 200 || sum1.Instance != "Testkunde" || len(sum1.Devices) != 3 {
		t.Errorf("summary %d %+v", code, sum1)
	}
	if code := ext("/api/v1/summary", "", nil); code != 401 {
		t.Errorf("summary without token: %d", code)
	}
	if code := ext("/api/v1/devices/"+reg.DeviceID, tok.Token, nil); code != 200 {
		t.Errorf("v1 device: %d", code)
	}
	if code := ext("/api/v1/devices/nope", tok.Token, nil); code != 404 {
		t.Errorf("v1 unknown device: %d", code)
	}
	if code := ext("/api/v1/alerts?state=all", tok.Token, nil); code != 200 {
		t.Errorf("v1 alerts: %d", code)
	}
	ui.must("DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), nil, nil)
	if code := ext("/api/v1/devices", tok.Token, nil); code != 401 {
		t.Errorf("deleted token: %d", code)
	}

	// ---- system, password, delete ----
	var sys map[string]any
	ui.must("GET", "/api/system", nil, &sys)
	if sys["version"] != "1.2.3" || sys["devices"] != 3.0 || sys["data_dir"] != dir || sys["demo"] != false {
		t.Errorf("system %v", sys)
	}
	if code := ui.do("POST", "/api/account/password", map[string]string{"old": "falsch-falsch", "new": "neues-passwort"}, &fe); code != 422 || fe.Fields["old"] == "" {
		t.Errorf("wrong old password: %d", code)
	}
	ui.must("POST", "/api/account/password", map[string]string{"old": "geheim-geheim", "new": "neues-passwort"}, nil)
	ui.must("GET", "/api/overview", nil, nil) // the current session survives
	ui.must("DELETE", "/api/devices/"+reg.DeviceID, nil, nil)
	if _, err := ac.Report(ctx, rep); err == nil {
		t.Error("report of a deleted agent accepted")
	}
	if code := ui.do("GET", "/api/devices/"+reg.DeviceID, nil, nil); code != 404 {
		t.Errorf("deleted device: %d", code)
	}

	// SPA fallback with the real web UI.
	resp, err = anon.hc.Get(srv.URL + "/devices/" + netDev.ID)
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("SPA page: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
}
