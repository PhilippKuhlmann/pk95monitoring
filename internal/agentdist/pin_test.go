package agentdist

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

var testPin = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)) // contains + and /

func TestPinFromCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	cert := srv.Certificate()
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if got := PinFromCert(cert); got != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("pin %s", got)
	}
	for in, want := range map[string]string{
		testPin:                  testPin,
		"sha256//" + testPin:     testPin,
		" " + testPin + "\n":     testPin,
		"":                       "",
		"abc":                    "",
		testPin + "'; reboot; '": "",
		base64.StdEncoding.EncodeToString([]byte("short")): "",
	} {
		if got := cleanPin(in); got != want {
			t.Errorf("cleanPin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandsPinned(t *testing.T) {
	p := params
	p.PubKeyPin = testPin
	linux, windows := Commands(p, "k", bins(true, true, true))
	if !strings.HasPrefix(linux, "curl -fsSL -k --pinnedpubkey 'sha256//"+testPin+"' 'https://mon.example:8443/agent/install.sh' |") {
		t.Errorf("linux: %s", linux)
	}
	fp := "abcd" + strings.Repeat("ef", 30)
	for _, want := range []string{"GetRawCertData()", "[Security.Cryptography.SHA256]::Create()", "-eq '" + fp + "'"} {
		if !strings.Contains(windows, want) {
			t.Errorf("windows lacks %q: %s", want, windows)
		}
	}
	if strings.Contains(windows, "[bool]1") || strings.Contains(windows, "true") {
		t.Errorf("windows accepts every certificate: %s", windows)
	}
	checkWindowsQuoting(t, windows)

	// An invalid pin is dropped instead of breaking the command.
	p.PubKeyPin = "not a pin"
	if linux, _ := Commands(p, "k", bins(true, false, false)); strings.Contains(linux, "pinnedpubkey") {
		t.Errorf("invalid pin used: %s", linux)
	}
	// Without a fingerprint (CA certificate) nothing is relaxed.
	linux, windows = Commands(Params{ServerURL: "https://mon.example.com"}, "k", bins(true, false, true))
	if strings.Contains(linux, "pinnedpubkey") || strings.Contains(linux, " -k") || strings.Contains(windows, "Callback") {
		t.Errorf("CA certificate: %s / %s", linux, windows)
	}
}

func TestInstallScriptsPinned(t *testing.T) {
	p := params
	p.PubKeyPin = testPin
	sh, err := InstallScript("sh", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local pin='" + testPin + "'", `opts+=(--pinnedpubkey "sha256//$pin")`, `curl "${opts[@]}"`} {
		if !bytes.Contains(sh, []byte(want)) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	ps, err := InstallScript("ps1", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$cert.GetRawCertData()", "[Security.Cryptography.SHA256]::Create()", "return $hex -eq $pin", "GetNewClosure()"} {
		if !bytes.Contains(ps, []byte(want)) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
	if bytes.Contains(ps, []byte("{ $true }")) {
		t.Error("install.ps1 still accepts every certificate")
	}
	if sh, _ := InstallScript("sh", params); !bytes.Contains(sh, []byte("local pin=''")) {
		t.Error("install.sh without pin")
	}
}

// TestShellScriptPinnedDownload runs install.sh against a TLS server with a self-signed
// certificate: with the right pin the download succeeds (and then stops at the wrong
// checksum), with a wrong pin curl refuses the connection.
func TestShellScriptPinnedDownload(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/download/linux/amd64" {
			downloads.Add(1)
			w.Write([]byte("binary"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cert := srv.Certificate()
	fpSum := sha256.Sum256(cert.Raw)

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
	wrongSum := sum([]byte("other"))
	run := func(pin string) string {
		sh, err := InstallScript("sh", Params{ServerURL: srv.URL, Fingerprint: hex.EncodeToString(fpSum[:]), PubKeyPin: pin})
		if err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "install.sh")
		os.WriteFile(script, sh, 0o600)
		cmd := exec.Command(bash, script, "--key", "k", "--sha256-amd64", wrongSum)
		cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("script succeeded with a wrong checksum:\n%s", out)
		}
		return string(out)
	}

	if out := run(PinFromCert(cert)); !strings.Contains(out, "Prüfsumme stimmt nicht") {
		if strings.Contains(out, "Download fehlgeschlagen") && downloads.Load() == 0 {
			t.Skipf("curl does not support --pinnedpubkey here:\n%s", out)
		}
		t.Fatalf("right pin: %s", out)
	}
	if out := run(testPin); !strings.Contains(out, "Download fehlgeschlagen") {
		t.Fatalf("wrong pin accepted:\n%s", out)
	}
	if n := downloads.Load(); n != 1 {
		t.Errorf("downloads %d, want 1", n)
	}
}
