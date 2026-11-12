package netcheck

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// trust makes the certificate of srv a trusted root for the duration of the test.
func trust(t *testing.T, srv *httptest.Server) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	setRoots(t, pool)
}

func setRoots(t *testing.T, pool *x509.CertPool) {
	old := rootCAs
	rootCAs = pool
	t.Cleanup(func() { rootCAs = old })
}

// tlsServer starts an HTTPS test server that does not log handshake errors.
func tlsServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// closedPort returns a localhost address where nothing listens.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestTCP(t *testing.T) {
	ln := listen(t)
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	t.Run("open", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "tcp", Target: ln.Addr().String()})
		expectState(t, c, model.StateOK, "Port offen")
		if _, ok := metric(c, "tcp.time"); !ok {
			t.Fatal("tcp.time missing")
		}
	})
	t.Run("device host", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "tcp", Target: port})
		expectState(t, c, model.StateOK, "Port offen")
		if c.Name != "TCP 127.0.0.1:"+port {
			t.Fatalf("Name = %q", c.Name)
		}
	})
	t.Run("refused", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "tcp", Target: closedPort(t)})
		expectState(t, c, model.StateCrit, "Verbindung abgelehnt")
		if len(c.Metrics) != 0 {
			t.Fatalf("metrics on failure: %v", c.Metrics)
		}
	})
}

func TestTLS(t *testing.T) {
	srv := tlsServer(t, func(http.ResponseWriter, *http.Request) {})
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)

	t.Run("untrusted", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "tls", Target: addr})
		expectState(t, c, model.StateCrit, "unbekannte Zertifizierungsstelle")
		if _, ok := metric(c, "cert.days"); !ok {
			t.Fatal("cert.days missing")
		}
	})
	t.Run("trusted", func(t *testing.T) {
		trust(t, srv)
		c := runCheck(model.NetCheck{Type: "tls", Target: addr})
		expectState(t, c, model.StateOK, "Gültig bis")
		if days, ok := metric(c, "cert.days"); !ok || days < 300 {
			t.Fatalf("cert.days = %v", days)
		}
		for _, f := range []string{"Aussteller", "Namen", "Zertifikat gültig bis"} {
			if c.Facts[f] == "" {
				t.Errorf("fact %q missing: %v", f, c.Facts)
			}
		}
		if !strings.Contains(c.Facts["Namen"], "127.0.0.1") {
			t.Errorf("Namen = %q", c.Facts["Namen"])
		}
	})
	t.Run("wrong name", func(t *testing.T) {
		trust(t, srv)
		c := runCheck(model.NetCheck{Type: "tls", Target: "localhost:" + port})
		expectState(t, c, model.StateCrit, "nicht für localhost")
	})
	t.Run("refused", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "tls", Target: closedPort(t)})
		expectState(t, c, model.StateCrit, "Verbindung abgelehnt")
	})
}

func TestTLSExpired(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA", Organization: []string{"PK95"}},
		NotBefore:             time.Now().Add(-60 * 24 * time.Hour),
		NotAfter:              time.Now().Add(-5*24*time.Hour - time.Hour),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	setRoots(t, pool)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()

	c := runCheck(model.NetCheck{Type: "tls", Target: ln.Addr().String()})
	expectState(t, c, model.StateCrit, "Zertifikat ungültig: abgelaufen am")
	if days, _ := metric(c, "cert.days"); days != -6 {
		t.Fatalf("cert.days = %v, want -6", days)
	}
	if c.Facts["Aussteller"] != "Test CA (PK95)" {
		t.Fatalf("Aussteller = %q", c.Facts["Aussteller"])
	}
}
