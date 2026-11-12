package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func writeExe(t *testing.T, content string) string {
	t.Helper()
	name := "pk95agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func updateServer(t *testing.T, payload string) (*Client, *atomic.Int32) {
	var hits atomic.Int32
	srv, fp := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/download/"+runtime.GOOS+"/"+runtime.GOARCH {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write([]byte(payload))
	})
	c, err := NewClient(srv.URL, "tok", fp, false)
	if err != nil {
		t.Fatal(err)
	}
	return c, &hits
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestUpdateReplacesBinary(t *testing.T) {
	c, _ := updateServer(t, "new binary")
	exe := writeExe(t, "old binary")
	var checked string
	u := &Updater{Client: c, Exe: exe, Version: "1.0.0", Log: quietLog(),
		Check: func(path, version string) error { checked = readFile(t, path) + "@" + version; return nil }}
	err := u.Apply(context.Background(), model.AgentUpdate{Version: "1.1.0",
		URL: "/agent/download/" + runtime.GOOS + "/" + runtime.GOARCH, SHA256: strings.ToUpper(sha("new binary"))})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("want ErrRestart, got %v", err)
	}
	if got := readFile(t, exe); got != "new binary" {
		t.Errorf("binary content %q", got)
	}
	if checked != "new binary@1.1.0" {
		t.Errorf("check saw %q", checked)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(exe); st.Mode().Perm()&0o111 == 0 {
			t.Error("new binary not executable")
		}
	}
	assertNoTemp(t, exe)
}

func TestUpdateRejectsWrongHash(t *testing.T) {
	c, hits := updateServer(t, "evil binary")
	exe := writeExe(t, "old binary")
	now := time.Unix(1_700_000_000, 0)
	u := &Updater{Client: c, Exe: exe, Version: "1.0.0", Log: quietLog(), Now: func() time.Time { return now },
		Check: func(string, string) error { t.Error("check must not run on a hash mismatch"); return nil }}
	upd := model.AgentUpdate{Version: "1.1.0", URL: "/agent/download/" + runtime.GOOS + "/" + runtime.GOARCH, SHA256: sha("good binary")}
	err := u.Apply(context.Background(), upd)
	if err == nil || errors.Is(err, ErrRestart) || !strings.Contains(err.Error(), "Prüfsumme") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if readFile(t, exe) != "old binary" {
		t.Error("binary replaced despite wrong hash")
	}
	assertNoTemp(t, exe)

	// Backoff: the same version is not downloaded again right away …
	if err := u.Apply(context.Background(), upd); err != nil || hits.Load() != 1 {
		t.Fatalf("retried too early: err=%v hits=%d", err, hits.Load())
	}
	// … but after the backoff.
	now = now.Add(7 * time.Hour)
	_ = u.Apply(context.Background(), upd)
	if hits.Load() != 2 {
		t.Errorf("not retried after backoff: hits=%d", hits.Load())
	}
}

func TestUpdateNoops(t *testing.T) {
	c, hits := updateServer(t, "x")
	exe := writeExe(t, "old")
	u := &Updater{Client: c, Exe: exe, Version: "1.0.0", Log: quietLog()}
	for _, upd := range []model.AgentUpdate{
		{Version: "1.0.0", URL: "/x", SHA256: sha("x")}, // same version
		{Version: "", URL: "/x", SHA256: sha("x")},
		{Version: "2.0.0", URL: "", SHA256: sha("x")},
	} {
		if err := u.Apply(context.Background(), upd); err != nil {
			t.Errorf("%+v: %v", upd, err)
		}
	}
	if hits.Load() != 0 {
		t.Error("downloaded although nothing to do")
	}
	if err := u.Apply(context.Background(), model.AgentUpdate{Version: "2.0.0", URL: "/x", SHA256: ""}); err == nil {
		t.Error("update without hash accepted")
	}
}

func TestUpdateFailedCheckKeepsBinary(t *testing.T) {
	c, _ := updateServer(t, "new")
	exe := writeExe(t, "old")
	u := &Updater{Client: c, Exe: exe, Version: "1.0.0", Log: quietLog(),
		Check: func(string, string) error { return errors.New("exec format error") }}
	err := u.Apply(context.Background(), model.AgentUpdate{Version: "1.1.0",
		URL: "/agent/download/" + runtime.GOOS + "/" + runtime.GOARCH, SHA256: sha("new")})
	if err == nil || errors.Is(err, ErrRestart) || readFile(t, exe) != "old" {
		t.Fatalf("err=%v content=%q", err, readFile(t, exe))
	}
	assertNoTemp(t, exe)
}

func assertNoTemp(t *testing.T, exe string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".pk95agent-update-*"))
	if len(m) > 0 {
		t.Errorf("temp files left: %v", m)
	}
}

func TestCheckVersionOutput(t *testing.T) {
	plat := runtime.GOOS + "/" + runtime.GOARCH
	other := "plan9/mips"
	if plat == other {
		other = "linux/amd64"
	}
	for _, tc := range []struct {
		out, version string
		ok           bool
	}{
		{"pk95agent 1.2.1 " + plat + "\n", "1.2.1", true},
		{"pk95agent 1.2.10 " + plat, "1.2.1", false}, // substring must not match
		{"pk95agent 1.2.1 " + other, "1.2.1", false}, // wrong platform
		{"pk95agent 1.2.1", "1.2.1", false},
		{"bash: syntax error 1.2.1 " + plat, "1.2.1", false},
		{"", "1.2.1", false},
	} {
		if err := checkVersionOutput(tc.out, tc.version); (err == nil) != tc.ok {
			t.Errorf("checkVersionOutput(%q, %q) = %v", tc.out, tc.version, err)
		}
	}
}
