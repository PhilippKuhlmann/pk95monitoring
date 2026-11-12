package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent.json")
	in := Config{
		Server: "https://mon.example:8443", Token: "tok", DeviceID: "dev1",
		Fingerprint: "ab:cd", Insecure: true, Interval: 30,
		Services: []string{"nginx", "postgresql"}, WatchAutoServices: true, IgnoreServices: []string{"foo*"},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Server != in.Server || out.Token != in.Token || out.DeviceID != in.DeviceID || out.Fingerprint != in.Fingerprint ||
		out.Insecure != in.Insecure || out.Interval != in.Interval || !slices.Equal(out.Services, in.Services) ||
		out.WatchAutoServices != in.WatchAutoServices || !slices.Equal(out.IgnoreServices, in.IgnoreServices) {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", st.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestLoadRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(path, []byte(`{"server":"https://x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("config without token accepted")
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("broken JSON accepted")
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}

func TestIntervalSeconds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 60}, {-5, 60}, {3, 10}, {30, 30}, {99999, 3600}} {
		if got := (Config{Interval: tc.in}).IntervalSeconds(); got != tc.want {
			t.Errorf("IntervalSeconds(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestApplyReply(t *testing.T) {
	c := Config{Interval: 60}
	if c.applyReply(0, nil, false, []string{}) {
		t.Error("empty reply reported a change")
	}
	if !c.applyReply(30, []string{"sshd"}, true, []string{"x"}) {
		t.Fatal("change not detected")
	}
	if c.Interval != 30 || !slices.Equal(c.Services, []string{"sshd"}) || !c.WatchAutoServices || !slices.Equal(c.IgnoreServices, []string{"x"}) {
		t.Errorf("not applied: %+v", c)
	}
	if c.applyReply(30, []string{"sshd"}, true, []string{"x"}) {
		t.Error("identical reply reported a change")
	}
	if !c.applyReply(30, nil, true, []string{"x"}) || c.Services != nil {
		t.Error("removing all services not applied")
	}
}

func TestMachineIDStable(t *testing.T) {
	a, b := MachineID(), MachineID()
	if a == "" || a != b {
		t.Fatalf("MachineID %q / %q", a, b)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	p := DefaultConfigPath()
	if filepath.Base(p) != "agent.json" {
		t.Errorf("unexpected path %s", p)
	}
	if BufferPath(p) != filepath.Join(filepath.Dir(p), "buffer.jsonl") {
		t.Errorf("buffer path %s", BufferPath(p))
	}
}

func TestSaveCreatesPrivateDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "agent.json")
	if err := Save(path, Config{Server: "https://s", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // ACLs instead of mode bits (secure_windows.go)
	}
	for p, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", p, st.Mode().Perm(), want)
		}
	}
}
