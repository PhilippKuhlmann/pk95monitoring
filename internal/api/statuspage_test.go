package api

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/web"
)

func TestStatusPageHTTP(t *testing.T) {
	dir := t.TempDir()
	c := newTestCore(t, dir)
	os.WriteFile(SetupCodePath(dir), []byte("ABCD-EFGH\n"), 0o600)
	if err := c.ImportDevice(model.Device{ID: "srv-secret", Name: "fs-geheim", Type: model.TypeAgent, Host: "10.1.2.3", Enabled: true,
		Group: "Server", Agent: &model.AgentConfig{AgentID: "m1", OS: "linux", Arch: "amd64"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(c, web.FS, Options{}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	ui := &client{t: t, base: srv.URL, hc: &http.Client{Jar: jar}}
	anon := &client{t: t, base: srv.URL, hc: srv.Client()}
	ui.must("POST", "/api/setup", map[string]string{"code": "abcd-efgh", "username": "admin", "password": "geheim-geheim"}, nil)

	var out statusPageOut
	ui.must("GET", "/api/statuspage", nil, &out)
	if out.Path != "" || out.Page.Enabled || out.Preview.State != core.PubUnknown {
		t.Fatalf("initial %+v", out)
	}
	var fe struct {
		Fields map[string]string `json:"fields"`
	}
	if code := ui.do("PUT", "/api/statuspage", map[string]any{"enabled": true, "components": []map[string]any{{"name": ""}}}, &fe); code != 422 ||
		fe.Fields["components.0.name"] == "" || fe.Fields["components.0.targets"] == "" {
		t.Fatalf("invalid page: %d %v", code, fe)
	}
	ui.must("PUT", "/api/statuspage", map[string]any{"enabled": true, "title": "Muster GmbH",
		"components": []map[string]any{{"name": "Dateiablage", "folders": []string{"Server"}}}}, &out)
	if !strings.HasPrefix(out.Path, "/status/") || out.Preview.Components[0].Members != 1 {
		t.Fatalf("saved %+v", out)
	}

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := get(out.Path)
	if resp.StatusCode != 200 || !strings.Contains(body, `src="/status.js"`) || resp.Header.Get("X-Robots-Tag") == "" ||
		resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := get("/status/wrong-token"); resp.StatusCode != 404 {
		t.Errorf("wrong token page: %d", resp.StatusCode)
	}
	token := strings.TrimPrefix(out.Path, "/status/")
	var pub core.PublicStatus
	anon.must("GET", "/api/status/"+token, nil, &pub)
	if pub.Title != "Muster GmbH" || len(pub.Components) != 1 || pub.Components[0].Members != 0 {
		t.Fatalf("public %+v", pub)
	}
	_, raw := get("/api/status/" + token)
	for _, secret := range []string{"srv-secret", "fs-geheim", "10.1.2.3", token} {
		if strings.Contains(raw, secret) {
			t.Errorf("public data contains %q", secret)
		}
	}
	if code := anon.do("GET", "/api/status/wrong-token", nil, nil); code != 404 {
		t.Errorf("wrong token data: %d", code)
	}
	if code := anon.do("GET", "/api/statuspage", nil, nil); code != 401 {
		t.Errorf("configuration without login: %d", code)
	}

	// a new link invalidates the old one
	ui.must("POST", "/api/statuspage/token", nil, &out)
	if code := anon.do("GET", "/api/status/"+token, nil, nil); code != 404 {
		t.Errorf("old token after rotate: %d", code)
	}
	if resp, _ := get(out.Path); resp.StatusCode != 200 {
		t.Errorf("new link: %d", resp.StatusCode)
	}
}
