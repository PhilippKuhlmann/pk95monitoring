package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.19.0", "0.18.0", true}, {"v1.0", "0.99.9", true}, {"0.18.0", "0.18.0", false}, {"0.9.0", "0.18.0", false}, {"0.19.0", "dev", false}} {
		if got := NewerVersion(c.a, c.b); got != c.want {
			t.Errorf("%s > %s = %v", c.a, c.b, got)
		}
	}
}

func fakeGitHub(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+UpdateRepo+"/releases/latest" || !strings.HasPrefix(r.UserAgent(), "pk95monitoring/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q,"body":"## Neu\n- Aktualisierung","html_url":"https://example.org/r","published_at":"2026-11-14T10:00:00Z"}`, tag)
	}))
	t.Cleanup(srv.Close)
	old := GitHubAPI
	GitHubAPI = srv.URL
	t.Cleanup(func() { GitHubAPI = old })
}

func TestSelfUpdate(t *testing.T) {
	dir := t.TempDir()
	e := newEnv(t, Options{DataDir: dir}) // version 1.2.0
	fakeGitHub(t, "v1.3.0")
	info, err := e.c.CheckUpdate(context.Background())
	if err != nil || !info.Available || info.Latest != "1.3.0" || info.Tag != "v1.3.0" || info.Notes == "" || info.Helper || info.CanInstall || !info.AutoCheck {
		t.Fatalf("check: %+v %v", info, err)
	}
	if err := e.c.InstallUpdate(); err == nil || !strings.Contains(err.Error(), "Update-Helfer") {
		t.Fatalf("install without helper: %v", err)
	}
	// the host helper is installed
	upd := filepath.Join(dir, "update")
	if err := os.MkdirAll(upd, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(upd, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("helper.json", `{"version":1,"mode":"compose","installed":1}`)
	if info := e.c.Update(); !info.CanInstall || info.HelperMode != "compose" {
		t.Fatalf("with helper: %+v", info)
	}
	if err := e.c.InstallUpdate(); err != nil {
		t.Fatal(err)
	}
	var req struct{ Tag string }
	b, _ := os.ReadFile(filepath.Join(upd, "request.json"))
	if json.Unmarshal(b, &req) != nil || req.Tag != "v1.3.0" {
		t.Fatalf("request %s", b)
	}
	if info := e.c.Update(); info.Phase != "requested" || info.CanInstall {
		t.Fatalf("requested: %+v", info)
	}
	if err := e.c.InstallUpdate(); err == nil {
		t.Fatal("second request accepted")
	}
	// the helper took it and failed
	os.Remove(filepath.Join(upd, "request.json"))
	write("status.json", `{"state":"failed","tag":"v1.3.0","started":10,"finished":20,"code":1}`)
	write("update.log", "pulling …\nError: manifest unknown")
	info = e.c.Update()
	if info.Phase != "failed" || !strings.Contains(info.Log, "manifest unknown") || !info.CanInstall {
		t.Fatalf("failed: %+v", info)
	}
	e.c.updateTick(context.Background())
	e.c.updateTick(context.Background()) // reported once
	titles := eventTitleList(t, e)
	if n := strings.Count(strings.Join(titles, "|"), "Update auf 1.3.0 fehlgeschlagen"); n != 1 {
		t.Fatalf("failure events %d: %v", n, titles)
	}
	// done into the running version
	write("status.json", `{"state":"done","tag":"v1.2.0","started":30,"finished":40,"code":0}`)
	e.c.updateTick(context.Background())
	if titles := eventTitleList(t, e); !strings.Contains(strings.Join(titles, "|"), "Auf Version 1.2.0 aktualisiert") {
		t.Fatalf("done: %v", titles)
	}
	if err := e.c.SetUpdateAutoCheck(false); err != nil || e.c.Update().AutoCheck {
		t.Fatal("auto check not off")
	}
	if !strings.Contains(strings.Join(eventTitleList(t, e), "|"), "Version 1.3.0 verfügbar") {
		t.Fatal("no 'available' event")
	}
}

func eventTitleList(t *testing.T, e *env) []string {
	t.Helper()
	evs, err := e.st.Events(context.Background(), "info", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Title)
	}
	return out
}
