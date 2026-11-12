package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"pk95monitoring/internal/model"
)

func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	return srv, hex.EncodeToString(sum[:])
}

func colonUpper(fp string) string {
	var parts []string
	for i := 0; i < len(fp); i += 2 {
		parts = append(parts, strings.ToUpper(fp[i:i+2]))
	}
	return strings.Join(parts, ":")
}

func TestNormalizeFingerprint(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{fp, fp, true},
		{colonUpper(fp), fp, true},
		{"  " + strings.ToUpper(fp) + " ", fp, true},
		{"abcd", "", false},
		{strings.Repeat("zz", 32), "", false},
	} {
		got, err := NormalizeFingerprint(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("NormalizeFingerprint(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestFingerprintPinning(t *testing.T) {
	srv, fp := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "pk95agent/") {
			t.Errorf("User-Agent %q", ua)
		}
		w.Write([]byte(`{"interval":30}`))
	})
	wrong := strings.Repeat("00", 32)
	for _, tc := range []struct {
		name     string
		fp       string
		insecure bool
		ok       bool
	}{
		{"pinned hex", fp, false, true},
		{"pinned colons upper-case", colonUpper(fp), false, true},
		{"wrong pin", wrong, false, false},
		{"wrong pin with insecure", wrong, true, false},
		{"system CAs reject self-signed", "", false, false},
		{"insecure", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(srv.URL, "tok", tc.fp, tc.insecure)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := c.Report(context.Background(), model.AgentReport{})
			if tc.ok && (err != nil || reply.Interval != 30) {
				t.Fatalf("want success, got %v (%+v)", err, reply)
			}
			if !tc.ok && err == nil {
				t.Fatal("connection accepted")
			}
		})
	}
}

func TestClientStatusErrors(t *testing.T) {
	var code atomic.Int32
	srv, fp := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
		w.Write([]byte(`{"error":"kaputt"}`))
	})
	c, _ := NewClient(srv.URL, "tok", fp, false)
	for _, tc := range []struct {
		code               int
		permanent, unauthz bool
	}{
		{400, true, false}, {404, true, false}, {401, false, true}, {408, false, false},
		{429, false, false}, {500, false, false}, {503, false, false},
	} {
		code.Store(int32(tc.code))
		_, err := c.Report(context.Background(), model.AgentReport{})
		if err == nil {
			t.Fatalf("%d: no error", tc.code)
		}
		if IsPermanent(err) != tc.permanent {
			t.Errorf("%d: permanent=%v", tc.code, IsPermanent(err))
		}
		if errors.Is(err, ErrUnauthorized) != tc.unauthz {
			t.Errorf("%d: unauthorized mismatch: %v", tc.code, err)
		}
	}
}

func TestTLSConfigRejectsBadFingerprint(t *testing.T) {
	if _, err := NewHTTPClient("xyz", false, 0); err == nil {
		t.Error("invalid fingerprint accepted")
	}
}

// TestDownloadTokenOnlyToServer: an absolute update URL on another host must not receive
// the agent's bearer token.
func TestDownloadTokenOnlyToServer(t *testing.T) {
	var ownAuth, foreignAuth atomic.Value
	own, fp := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		ownAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte("own"))
	})
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte("foreign"))
	}))
	t.Cleanup(foreign.Close)

	c, err := NewClient(own.URL, "secret-token", fp, false)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if _, err := c.Download(context.Background(), "/agent/download/linux/amd64", &sb, 1<<20); err != nil || sb.String() != "own" {
		t.Fatalf("own download: %q %v", sb.String(), err)
	}
	if got := ownAuth.Load(); got != "Bearer secret-token" {
		t.Errorf("server got Authorization %q", got)
	}
	sb.Reset()
	if _, err := c.Download(context.Background(), foreign.URL+"/bin", &sb, 1<<20); err != nil || sb.String() != "foreign" {
		t.Fatalf("foreign download: %q %v", sb.String(), err)
	}
	if got := foreignAuth.Load(); got != "" {
		t.Errorf("token leaked to foreign host: %q", got)
	}
	// Size limit.
	sb.Reset()
	if _, err := c.Download(context.Background(), "/x", &sb, 2); err == nil {
		t.Error("download over the limit accepted")
	}
}
