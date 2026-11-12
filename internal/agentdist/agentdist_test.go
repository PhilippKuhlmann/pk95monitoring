package agentdist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestCatalog(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir, "1.2.3")
	if c.Version() != "1.2.3" {
		t.Error("version")
	}
	bins := c.Binaries()
	if len(bins) != 3 || bins[0].File != "pk95agent-linux-amd64" || bins[1].File != "pk95agent-linux-arm64" ||
		bins[2].File != "pk95agent-windows-amd64.exe" {
		t.Fatalf("targets %+v", bins)
	}
	for _, b := range bins {
		if b.Available {
			t.Errorf("%s available in an empty dir", b.File)
		}
	}

	amd := []byte("linux amd64 binary")
	path := filepath.Join(dir, "pk95agent-linux-amd64")
	if err := os.WriteFile(path, amd, 0o755); err != nil {
		t.Fatal(err)
	}
	b, p, ok := c.Lookup("linux", "amd64")
	if !ok || p != path || b.SHA256 != sum(amd) || b.Size != int64(len(amd)) || !b.Available {
		t.Fatalf("lookup %+v %s %v", b, p, ok)
	}
	if _, _, ok := c.Lookup("linux", "arm64"); ok {
		t.Error("missing arm64 reported available")
	}
	if _, _, ok := c.Lookup("darwin", "arm64"); ok {
		t.Error("darwin is not distributed")
	}

	// Replaced file (same size, new mtime) is rehashed.
	amd2 := []byte("LINUX AMD64 BINARY")
	if err := os.WriteFile(path, amd2, 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	os.Chtimes(path, later, later)
	if b, _, _ := c.Lookup("linux", "amd64"); b.SHA256 != sum(amd2) {
		t.Errorf("not rehashed after change: %s", b.SHA256)
	}

	js, _ := json.Marshal(c.Binaries()[0])
	for _, k := range []string{`"os":"linux"`, `"arch":"amd64"`, `"file":`, `"size":18`, `"sha256":`, `"available":true`} {
		if !bytes.Contains(js, []byte(k)) {
			t.Errorf("JSON %s lacks %s", js, k)
		}
	}
}

var params = Params{ServerURL: "https://mon.example:8443/", Fingerprint: "AB:CD:" + strings.Repeat("EF", 30), Version: "1.2.3"}

func TestInstallScripts(t *testing.T) {
	sh, err := InstallScript("sh", params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local server='https://mon.example:8443'", "local fingerprint='abcd" + strings.Repeat("ef", 30) + "'",
		"/agent/download/linux/$arch", "sha256sum", "/usr/local/bin/pk95agent", "main \"$@\""} {
		if !bytes.Contains(sh, []byte(want)) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	ps, err := InstallScript("ps1", params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$Server = 'https://mon.example:8443'", "/agent/download/windows/amd64", "Get-FileHash",
		"PK95Agent", "pk95agent.exe", "& $Exe @installArgs", "\r\n"} {
		if !bytes.Contains(ps, []byte(want)) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
	for i, r := range string(ps) {
		if r > 127 {
			t.Fatalf("install.ps1 has non-ASCII %q at %d", r, i)
		}
	}
	if !bytes.HasPrefix(ps, []byte("#")) || bytes.Index(ps, []byte("param(")) < 0 {
		t.Error("ps1 must start with comments and a param block")
	}
	if _, err := InstallScript("bat", params); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := InstallScript("sh", Params{}); err == nil {
		t.Error("missing server URL accepted")
	}
}

func TestInstallScriptInjection(t *testing.T) {
	evil := Params{ServerURL: "https://x'; rm -rf / #\n$(reboot)`id`\"%PATH%"}
	sh, err := InstallScript("sh", evil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"rm -rf", "$(reboot)", "`id`", "%PATH%"} {
		if bytes.Contains(sh, []byte(bad)) {
			t.Errorf("unsafe text %q survived", bad)
		}
	}
	if bytes.Contains(sh, []byte("https://x'")) {
		t.Error("quote not removed")
	}
}

func TestShellSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	for _, p := range []Params{params, {ServerURL: "http://10.0.0.5:8080"}} {
		sh, err := InstallScript("sh", p)
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(t.TempDir(), "install.sh")
		os.WriteFile(f, sh, 0o600)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
}

// TestShellScriptRefuses runs the rendered script with stubbed id/uname: it must refuse a
// missing or wrong checksum before touching the system.
func TestShellScriptRefuses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	payload := []byte("binary")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/download/linux/amd64" {
			downloads.Add(1)
			w.Write(payload)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stubs := t.TempDir()
	write := func(name, body string) {
		os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	write("id", "echo 0")
	write("uname", "echo x86_64")
	write("systemctl", "exit 0")
	if _, err := exec.LookPath("sha256sum"); err != nil {
		write("sha256sum", `shasum -a 256 "$@"`)
	}
	sh, err := InstallScript("sh", Params{ServerURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "install.sh")
	os.WriteFile(script, sh, 0o600)

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bash, append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing key", []string{"--sha256-amd64", sum(payload)}, "--key fehlt"},
		{"missing hash", []string{"--key", "k"}, "Keine SHA-256-Prüfsumme"},
		{"hash for other arch only", []string{"--key", "k", "--sha256-arm64", sum(payload)}, "Keine SHA-256-Prüfsumme"},
		{"invalid hash", []string{"--key", "k", "--sha256-amd64", "xyz"}, "Ungültige SHA-256-Prüfsumme"},
		{"wrong hash", []string{"--key", "k", "--sha256-amd64", sum([]byte("other"))}, "Prüfsumme stimmt nicht"},
		{"option without value", []string{"--key"}, "Wert für --key fehlt"},
		{"unknown option", []string{"--bogus", "x"}, "Unbekannte Option"},
	} {
		out, err := run(tc.args...)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Errorf("%s: err=%v output:\n%s", tc.name, err, out)
		}
	}
	if n := downloads.Load(); n != 1 {
		t.Errorf("downloads %d, want 1 (only the wrong-hash case downloads)", n)
	}
}

func bins(linuxAmd, linuxArm, win bool) []Binary {
	return []Binary{
		{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("a", 64), Available: linuxAmd},
		{OS: "linux", Arch: "arm64", SHA256: strings.Repeat("b", 64), Available: linuxArm},
		{OS: "windows", Arch: "amd64", SHA256: strings.Repeat("C", 64), Available: win},
	}
}

func TestCommandsSelfSigned(t *testing.T) {
	linux, windows := Commands(params, "KEY-123_abc", bins(true, true, true))
	fp := "abcd" + strings.Repeat("ef", 30)
	wantLinux := "curl -fsSL -k 'https://mon.example:8443/agent/install.sh' | $(command -v sudo) bash -s -- --key 'KEY-123_abc'" +
		" --fingerprint " + fp + " --sha256-amd64 " + strings.Repeat("a", 64) + " --sha256-arm64 " + strings.Repeat("b", 64)
	if linux != wantLinux {
		t.Errorf("linux:\n got %s\nwant %s", linux, wantLinux)
	}
	wantWin := `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "` +
		`[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; ` +
		`[Net.ServicePointManager]::ServerCertificateValidationCallback={ ([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash((Get-Variable args -ValueOnly)[1].GetRawCertData())) -replace '-') -eq '` + fp + `' }; ` +
		`& ([scriptblock]::Create((New-Object Net.WebClient).DownloadString('https://mon.example:8443/agent/install.ps1'))) ` +
		`-Key 'KEY-123_abc' -Fingerprint '` + fp + `' -Sha256 '` + strings.Repeat("c", 64) + `'"`
	if windows != wantWin {
		t.Errorf("windows:\n got %s\nwant %s", windows, wantWin)
	}
	checkWindowsQuoting(t, windows)
}

func TestCommandsCACertificate(t *testing.T) {
	linux, windows := Commands(Params{ServerURL: "https://mon.example.com"}, "k", bins(true, false, true))
	if strings.Contains(linux, " -k ") || strings.Contains(linux, "--fingerprint") || strings.Contains(linux, "arm64") {
		t.Errorf("linux: %s", linux)
	}
	if !strings.HasPrefix(linux, "curl -fsSL 'https://mon.example.com/agent/install.sh'") {
		t.Errorf("linux: %s", linux)
	}
	if strings.Contains(windows, "ServerCertificateValidationCallback") || strings.Contains(windows, "-Fingerprint") {
		t.Errorf("windows bypasses certificate check without a fingerprint: %s", windows)
	}
	checkWindowsQuoting(t, windows)
}

func TestCommandsUnavailable(t *testing.T) {
	linux, windows := Commands(params, "k", bins(false, false, false))
	if linux != "" || windows != "" {
		t.Errorf("commands without binaries: %q %q", linux, windows)
	}
	linux, windows = Commands(params, "k';reboot;'", bins(false, true, false))
	if windows != "" || !strings.Contains(linux, "--key 'kreboot' --fingerprint") {
		t.Errorf("linux=%q windows=%q", linux, windows)
	}
}

// checkWindowsQuoting: the -Command argument must survive cmd.exe and PowerShell unchanged.
func checkWindowsQuoting(t *testing.T, cmd string) {
	t.Helper()
	const prefix = `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "`
	if !strings.HasPrefix(cmd, prefix) || !strings.HasSuffix(cmd, `"`) {
		t.Fatalf("shape: %s", cmd)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(cmd, prefix), `"`)
	for _, bad := range []string{`"`, "$", "%", "`", "\n", "^"} {
		if strings.Contains(inner, bad) {
			t.Errorf("inner command contains %q: %s", bad, inner)
		}
	}
	if strings.Count(inner, "'")%2 != 0 || strings.Count(inner, "(") != strings.Count(inner, ")") ||
		strings.Count(inner, "{") != strings.Count(inner, "}") {
		t.Errorf("unbalanced quoting: %s", inner)
	}
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		check := "$e=$null; [void][System.Management.Automation.Language.Parser]::ParseInput($env:PK95CMD,[ref]$null,[ref]$e); if ($e) { $e; exit 1 }"
		c := exec.Command(pwsh, "-NoProfile", "-Command", check)
		c.Env = append(os.Environ(), "PK95CMD="+inner)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("PowerShell parser: %v\n%s", err, out)
		}
	}
}

func TestCommandsDevice(t *testing.T) {
	p := params
	p.DeviceID, p.Node = "demo-pve", "pve1"
	linux, windows := Commands(p, "k", bins(true, false, true))
	if !strings.HasSuffix(linux, " --sha256-amd64 "+strings.Repeat("a", 64)+" --device 'demo-pve' --node 'pve1'") {
		t.Errorf("linux: %s", linux)
	}
	if !strings.HasSuffix(windows, ` -Device 'demo-pve' -Node 'pve1'"`) {
		t.Errorf("windows: %s", windows)
	}
	checkWindowsQuoting(t, windows)

	// Node without device is ignored; unsafe characters are dropped.
	p.DeviceID, p.Node = "", "pve1"
	if linux, _ := Commands(p, "k", bins(true, false, false)); strings.Contains(linux, "--node") {
		t.Errorf("node without device: %s", linux)
	}
	p.DeviceID, p.Node = "ab'; reboot; '", "n$(id)`x`\"%P%"
	linux, windows = Commands(p, "k", bins(true, false, true))
	if !strings.Contains(linux, "--device 'abreboot' --node 'nidxP'") {
		t.Errorf("linux not sanitized: %s", linux)
	}
	if !strings.Contains(windows, "-Device 'abreboot' -Node 'nidxP'") {
		t.Errorf("windows not sanitized: %s", windows)
	}
	checkWindowsQuoting(t, windows)
}

func TestInstallScriptsDevice(t *testing.T) {
	sh, err := InstallScript("sh", params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local device='' node=''", `--device) need_value $# "$1"; device="$2"`,
		`args+=(--device "$device")`, `args+=(--node "$node")`} {
		if !bytes.Contains(sh, []byte(want)) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	ps, err := InstallScript("ps1", params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[string]$Device = ''", "[string]$Node = ''", "@('--device', $Device)", "@('--node', $Node)"} {
		if !bytes.Contains(ps, []byte(want)) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
}

// TestShellScriptPassesDevice runs the script up to "pk95agent install" (stubbed) and checks
// that --device and --node are passed through.
func TestShellScriptPassesDevice(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("bash not available")
	}
	sh, err := InstallScript("sh", Params{ServerURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	// Cut the script before the download and call the install step with a stub binary.
	body := string(sh)
	start := strings.Index(body, "\tlocal args=(install")
	end := strings.Index(body, "\t/usr/local/bin/pk95agent \"${args[@]}\"")
	if start < 0 || end < 0 {
		t.Fatal("install step not found")
	}
	parse := body[strings.Index(body, "main() {"):strings.Index(body, "\t[ \"$(id -u)\"")]
	script := "set -euo pipefail\ndie() { echo \"$*\" >&2; exit 1; }\nneed_value() { [ \"$1\" -ge 2 ] || die x; }\n" +
		parse + body[start:end] + "\tprintf '%s\\n' \"${args[@]}\"\n}\nmain \"$@\"\n"
	f := filepath.Join(t.TempDir(), "step.sh")
	os.WriteFile(f, []byte(script), 0o600)
	out, err := exec.Command(bash, f, "--key", "K", "--device", "demo-pve", "--node", "pve1").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.Fields(string(out)); strings.Join(got, " ") != "install --server http://127.0.0.1:1 --key K --device demo-pve --node pve1" {
		t.Errorf("args %q", got)
	}
}
