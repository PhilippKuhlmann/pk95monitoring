package netcheck

import (
	"context"
	"crypto/x509"
	"errors"
	"maps"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// metric returns the value of a metric and whether it exists.
func metric(c model.Check, name string) (float64, bool) {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}

// expectState fails the test unless the check has the wanted state and its message
// contains msg.
func expectState(t *testing.T, c model.Check, state, msg string) {
	t.Helper()
	if c.State != state || !strings.Contains(c.Message, msg) {
		t.Fatalf("got state %q msg %q, want %q containing %q", c.State, c.Message, state, msg)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		c    model.NetCheck
		host string
		want []string // fields with errors
	}{
		{"ping device host", model.NetCheck{Type: "ping"}, "10.0.0.1", nil},
		{"ping target", model.NetCheck{Type: "ping", Target: "nas.local"}, "", nil},
		{"ping ipv6", model.NetCheck{Type: "ping", Target: "fe80::1"}, "", nil},
		{"ping no host", model.NetCheck{Type: "ping"}, "", []string{"target"}},
		{"ping bad host", model.NetCheck{Type: "ping", Target: "a b"}, "", []string{"target"}},
		{"missing type", model.NetCheck{}, "h", []string{"type"}},
		{"unknown type", model.NetCheck{Type: "smtp"}, "h", []string{"type"}},
		{"http ok", model.NetCheck{Type: "http", Target: "https://example.org/x", ExpectStatus: 204, Keyword: "ok"}, "", nil},
		{"http head", model.NetCheck{Type: "http", Target: "http://example.org", Method: "head"}, "", nil},
		{"http no scheme", model.NetCheck{Type: "http", Target: "example.org"}, "", []string{"target"}},
		{"http ftp", model.NetCheck{Type: "http", Target: "ftp://example.org"}, "", []string{"target"}},
		{"http bad method", model.NetCheck{Type: "http", Target: "http://x", Method: "POST"}, "", []string{"method"}},
		{"http bad status", model.NetCheck{Type: "http", Target: "http://x", ExpectStatus: 999}, "", []string{"expect_status"}},
		{"http head keyword", model.NetCheck{Type: "http", Target: "http://x", Method: "HEAD", Keyword: "a"}, "", []string{"keyword"}},
		{"tcp ok", model.NetCheck{Type: "tcp", Target: "10.0.0.1:22"}, "", nil},
		{"tcp port only", model.NetCheck{Type: "tcp", Target: "3389"}, "srv", nil},
		{"tcp colon port", model.NetCheck{Type: "tcp", Target: ":443"}, "srv", nil},
		{"tcp ipv6", model.NetCheck{Type: "tcp", Target: "[::1]:22"}, "", nil},
		{"tcp no port", model.NetCheck{Type: "tcp", Target: "10.0.0.1"}, "", []string{"target"}},
		{"tcp bad port", model.NetCheck{Type: "tcp", Target: "host:70000"}, "", []string{"target"}},
		{"tcp nothing", model.NetCheck{Type: "tcp"}, "", []string{"target"}},
		{"tls default port", model.NetCheck{Type: "tls", Target: "example.org"}, "", nil},
		{"tls device host", model.NetCheck{Type: "tls"}, "pve.local", nil},
		{"tls nothing", model.NetCheck{Type: "tls"}, "", []string{"target"}},
		{"dns ok", model.NetCheck{Type: "dns", Target: "example.org.", RecordType: "mx", Server: "1.1.1.1"}, "", nil},
		{"dns server port", model.NetCheck{Type: "dns", Target: "example.org", Server: "10.0.0.53:5353"}, "", nil},
		{"dns no target", model.NetCheck{Type: "dns"}, "", []string{"target"}},
		{"dns bad name", model.NetCheck{Type: "dns", Target: "exa mple.org"}, "", []string{"target"}},
		{"dns bad type", model.NetCheck{Type: "dns", Target: "x.org", RecordType: "SRV"}, "", []string{"record_type"}},
		{"dns bad server", model.NetCheck{Type: "dns", Target: "x.org", Server: "1.1.1.1:0"}, "", []string{"server"}},
		{"timeout", model.NetCheck{Type: "ping", Target: "h", Timeout: 500}, "", []string{"timeout"}},
		{"negative timeout", model.NetCheck{Type: "ping", Target: "h", Timeout: -1}, "", []string{"timeout"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(tt.c, tt.host)
			got := slices.Sorted(maps.Keys(errs))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Validate errors = %v, want fields %v", errs, tt.want)
			}
			for f, msg := range errs {
				if msg == "" {
					t.Errorf("field %s has an empty message", f)
				}
			}
		})
	}
}

func TestDefaultName(t *testing.T) {
	tests := []struct {
		c    model.NetCheck
		host string
		want string
	}{
		{model.NetCheck{Type: "ping"}, "10.0.0.1", "Ping 10.0.0.1"},
		{model.NetCheck{Type: "ping", Target: "8.8.8.8"}, "10.0.0.1", "Ping 8.8.8.8"},
		{model.NetCheck{Type: "http", Target: "https://example.org"}, "", "HTTP https://example.org"},
		{model.NetCheck{Type: "http", Target: "https://admin:geheim@example.org/status"}, "", "HTTP https://admin:xxxxx@example.org/status"},
		{model.NetCheck{Type: "tcp", Target: "db"}, "", "TCP db"},
		{model.NetCheck{Type: "tcp", Target: "db:5432"}, "", "TCP db:5432"},
		{model.NetCheck{Type: "tcp", Target: "22"}, "srv", "TCP srv:22"},
		{model.NetCheck{Type: "tls", Target: "example.org"}, "", "Zertifikat example.org:443"},
		{model.NetCheck{Type: "tls"}, "pve", "Zertifikat pve:443"},
		{model.NetCheck{Type: "dns", Target: "example.org"}, "", "DNS example.org (A)"},
		{model.NetCheck{Type: "dns", Target: "example.org", RecordType: "mx"}, "", "DNS example.org (MX)"},
	}
	for _, tt := range tests {
		if got := DefaultName(tt.c, tt.host); got != tt.want {
			t.Errorf("DefaultName(%+v) = %q, want %q", tt.c, got, tt.want)
		}
	}
}

func TestRunInvalidConfig(t *testing.T) {
	c := Run(context.Background(), model.NetCheck{ID: "abc123", Type: "http", Target: "nope"}, "")
	if c.Key != "net:abc123" || c.Group != Group || c.Name != "HTTP nope" {
		t.Fatalf("unexpected identity: %+v", c)
	}
	expectState(t, c, model.StateUnknown, "Konfiguration ungültig")
}

func TestRunName(t *testing.T) {
	ln := listen(t)
	c := Run(context.Background(), model.NetCheck{ID: "x", Type: "tcp", Name: " Datenbank ", Target: ln.Addr().String()}, "")
	if c.Name != "Datenbank" {
		t.Fatalf("Name = %q", c.Name)
	}
}

func TestRunCanceled(t *testing.T) {
	ln := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := Run(ctx, model.NetCheck{ID: "x", Type: "tcp", Target: ln.Addr().String()}, "")
	expectState(t, c, model.StateUnknown, "abgebrochen")
}

// TestRunCanceledWithCause: a caller that cancels with its own cause still gets unknown.
func TestRunCanceledWithCause(t *testing.T) {
	ln := listen(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("shutdown"))
	c := Run(ctx, model.NetCheck{ID: "x", Type: "tcp", Target: ln.Addr().String()}, "")
	expectState(t, c, model.StateUnknown, "abgebrochen")
}

func TestEndpointMissingPort(t *testing.T) {
	_, err := endpoint("db.local", "", "")
	if err == nil || !strings.Contains(err.Error(), "Host:Port erforderlich") {
		t.Fatalf("err = %v", err)
	}
}

func TestCertDays(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		notAfter time.Time
		want     float64
	}{
		{now.Add(30*24*time.Hour + time.Hour), 30},
		{now.Add(23 * time.Hour), 0},
		{now.Add(-time.Hour), -1},
		{now.Add(-49 * time.Hour), -3},
	}
	for _, tt := range tests {
		if got := certDays(&x509.Certificate{NotAfter: tt.notAfter}, now); got != tt.want {
			t.Errorf("certDays(%v) = %v, want %v", tt.notAfter, got, tt.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	answers := []string{"Mail.Example.org.", "2001:db8::1", "v=spf1 -all"}
	for _, want := range []string{"mail.example.org", "MAIL.EXAMPLE.ORG.", "2001:0db8:0::1", "v=spf1 -all"} {
		if !matchAny(answers, want) {
			t.Errorf("matchAny(%q) = false", want)
		}
	}
	for _, want := range []string{"example.org", "2001:db8::2", "v=spf1"} {
		if matchAny(answers, want) {
			t.Errorf("matchAny(%q) = true", want)
		}
	}
}

// listen opens a TCP listener on localhost that accepts and closes connections.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			c.Close()
		}
	}()
	return ln
}
