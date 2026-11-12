package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/api"
	"pk95monitoring/internal/store"
)

func TestSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	c, err := loadOrCreateCert(dir, "mon.kunde.de")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.NotAfter.Before(time.Now().AddDate(9, 11, 0)) {
		t.Errorf("valid until %v", leaf.NotAfter)
	}
	for _, h := range []string{"localhost", "mon.kunde.de", "127.0.0.1"} {
		if err := leaf.VerifyHostname(h); err != nil {
			t.Errorf("SAN %s: %v", h, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key.pem: %v %v", fi.Mode(), err)
	}

	info := certInfo(c)
	if info.trusted {
		t.Error("self-signed certificate reported as trusted")
	}
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(info.fingerprint) {
		t.Errorf("fingerprint %q", info.fingerprint)
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if info.pin != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("pin %q", info.pin)
	}

	// The second start loads the same certificate (agents pin it).
	again, err := loadOrCreateCert(dir, "other.example")
	if err != nil || certInfo(again).fingerprint != info.fingerprint {
		t.Errorf("certificate replaced: %v", err)
	}
}

func TestBrokenCustomCertKept(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("garbage"), 0o644)
	if _, err := loadOrCreateCert(dir, ""); err == nil {
		t.Fatal("broken certificate accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cert.pem")); string(b) != "garbage" {
		t.Error("custom certificate overwritten")
	}
}

func TestCustomCert(t *testing.T) {
	// A certificate from a CA the system does not know is pinned like a self-signed one; the
	// fingerprint is that of the leaf.
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	c := srv.TLS.Certificates[0]
	info := certInfo(c)
	sum := sha256.Sum256(srv.Certificate().Raw)
	if info.trusted || info.fingerprint != formatFingerprint(sum[:]) {
		t.Errorf("info %+v", info)
	}
	if certInfo(tls.Certificate{}) != (tlsInfo{}) {
		t.Error("empty certificate")
	}
}

func TestHelpers(t *testing.T) {
	if got := formatFingerprint([]byte{0xab, 0x01, 0xff}); got != "AB:01:FF" {
		t.Errorf("format %q", got)
	}
	for in, want := range map[string]string{"https://mon.kunde.de:8443/": "mon.kunde.de", "https://[fd00::5]:8443": "fd00::5", "": "", "::bad": ""} {
		if got := publicHost(in); got != want {
			t.Errorf("publicHost(%q) = %q", in, got)
		}
	}
	tmpl := &x509.Certificate{}
	addSAN(tmpl, "Host.Example")
	addSAN(tmpl, "host.example")
	addSAN(tmpl, "[::1]")
	addSAN(tmpl, "::1")
	addSAN(tmpl, " ")
	if len(tmpl.DNSNames) != 1 || tmpl.DNSNames[0] != "host.example" || len(tmpl.IPAddresses) != 1 || !tmpl.IPAddresses[0].Equal(net.IPv6loopback) {
		t.Errorf("SANs %v %v", tmpl.DNSNames, tmpl.IPAddresses)
	}
	if !regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`).MatchString(newSetupCode()) || strings.ContainsAny(newSetupCode(), "01OI") {
		t.Error("setup code format")
	}
}

func TestRedirect(t *testing.T) {
	served := false
	h := redirectHandler(":8443", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served = true }))
	for _, tc := range []struct{ host, want string }{
		{"10.0.0.5", "https://10.0.0.5:8443/devices?x=1"},
		{"mon.example:80", "https://mon.example:8443/devices?x=1"},
		{"[fd00::1]:80", "https://[fd00::1]:8443/devices?x=1"},
	} {
		r := httptest.NewRequest("GET", "http://x/devices?x=1", nil)
		r.Host = tc.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != tc.want {
			t.Errorf("%s: %d %s", tc.host, rec.Code, rec.Header().Get("Location"))
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !served {
		t.Error("request from a TLS proxy redirected")
	}
	h443 := redirectHandler(":443", http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h443.ServeHTTP(rec, httptest.NewRequest("GET", "http://mon/", nil))
	if rec.Header().Get("Location") != "https://mon/" {
		t.Errorf("443: %s", rec.Header().Get("Location"))
	}
}

func TestSetupCode(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ensureSetupCode(st, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(api.SetupCodePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(string(b))
	if err := ensureSetupCode(st, dir); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(api.SetupCodePath(dir)); strings.TrimSpace(string(b2)) != code {
		t.Error("code changed on restart")
	}
	if _, err := st.CreateUser("admin", "x"); err != nil {
		t.Fatal(err)
	}
	if err := ensureSetupCode(st, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(api.SetupCodePath(dir)); !os.IsNotExist(err) {
		t.Error("setup code kept after setup")
	}
}

// TestRun starts the whole server (needs the real core; skipped while it is a placeholder).
func TestRun(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errc <- fmt.Errorf("panic: %v", r)
			}
		}()
		errc <- Run(ctx, Config{Version: "1.0.0", Listen: addr, DataDir: dir, AgentDir: dir})
	}()
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		select {
		case err := <-errc:
			cancel()
			if strings.Contains(fmt.Sprint(err), "not implemented") {
				t.Skipf("internal/core is not implemented yet: %v", err)
			}
			t.Fatalf("Run ended: %v", err)
		default:
		}
		if resp, err = hc.Get("https://" + addr + "/api/session"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("server not reachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS == nil {
		t.Errorf("session: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "cert.pem")); err != nil {
		t.Error("no certificate written")
	}
	if _, err := os.Stat(api.SetupCodePath(dir)); err != nil {
		t.Error("no setup code written")
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}
