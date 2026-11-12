package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckServerURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://mon.example:8443/", "https://mon.example:8443", true},
		{" https://10.0.0.5:8443 ", "https://10.0.0.5:8443", true},
		{"http://mon.local", "http://mon.local", true},
		{"mon.example", "", false},
		{"ftp://mon", "", false},
		{"", "", false},
		{"https://", "", false},
	} {
		got, err := checkServerURL(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("checkServerURL(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestCheckDevice(t *testing.T) {
	for _, tc := range []struct {
		dev, node, wantDev, wantNode string
		ok                           bool
	}{
		{"", "", "", "", true},
		{" demo-pve ", " pve1 ", "demo-pve", "pve1", true},
		{"abc123", "", "abc123", "", true},
		{"", "pve1", "", "", false},
		{"a b", "", "", "", false},
		{"abc", "pve\n1", "", "", false},
		{strings.Repeat("a", 65), "", "", "", false},
	} {
		d, n, err := checkDevice(tc.dev, tc.node)
		if (err == nil) != tc.ok || d != tc.wantDev || n != tc.wantNode {
			t.Errorf("checkDevice(%q, %q) = %q, %q, %v", tc.dev, tc.node, d, n, err)
		}
	}
}

func TestSamePath(t *testing.T) {
	if !samePath("/usr/local/bin/../bin/pk95agent", "/usr/local/bin/pk95agent") {
		t.Error("cleaned paths differ")
	}
	if samePath("/tmp/pk95agent", "/usr/local/bin/pk95agent") {
		t.Error("different paths equal")
	}
}

func TestSystemdUnit(t *testing.T) {
	for _, want := range []string{"Restart=always", "RestartSec=10", "After=network-online.target", "{{range Arguments}}"} {
		if !strings.Contains(systemdUnit, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	s, err := newService(filepath.Join(t.TempDir(), "agent.json"), installPath())
	if err != nil || s == nil {
		t.Fatalf("newService: %v", err)
	}
}

func TestInstallPath(t *testing.T) {
	p := installPath()
	switch runtime.GOOS {
	case "windows":
		if !strings.HasSuffix(p, `PK95Agent\pk95agent.exe`) {
			t.Error(p)
		}
	default:
		if p != "/usr/local/bin/pk95agent" {
			t.Error(p)
		}
	}
}

func TestRemoveConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pk95agent")
	cfg := filepath.Join(dir, "agent.json")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(cfg, []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, "buffer.jsonl"), []byte("{}\n"), 0o600)
	if !removeConfig(cfg) {
		t.Fatal("nothing removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("empty config directory left")
	}
}

// TestVersionCommand builds the binary with a version stamp; the self-update check relies on
// "pk95agent version" printing it.
func TestVersionCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	exe := filepath.Join(t.TempDir(), "pk95agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=1.2.3-test", "-o", exe, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(exe, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "pk95agent 1.2.3-test "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("version: %q %v", out, err)
	}
	out, err = exec.Command(exe, "bogus").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Aufruf: pk95agent") {
		t.Errorf("unknown command: %q %v", out, err)
	}
}
