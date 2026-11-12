package netcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func httpServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func runCheck(c model.NetCheck) model.Check {
	c.ID = "t1"
	return Run(context.Background(), c, "127.0.0.1")
}

func TestHTTP(t *testing.T) {
	var (
		mu               sync.Mutex
		gotUA, gotMethod string
	)
	srv := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUA, gotMethod = r.UserAgent(), r.Method
		mu.Unlock()
		switch r.URL.Path {
		case "/":
			io.WriteString(w, "<html>Willkommen bei PK95</html>")
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/error":
			http.Error(w, "kaputt", http.StatusInternalServerError)
		case "/r1":
			http.Redirect(w, r, "/r2", http.StatusFound)
		case "/r2":
			http.Redirect(w, r, "/", http.StatusMovedPermanently)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			io.WriteString(w, strings.Repeat("x", keywordLimit))
			io.WriteString(w, "late")
		default:
			http.NotFound(w, r)
		}
	})

	tests := []struct {
		name   string
		c      model.NetCheck
		state  string
		msg    string
		status float64
	}{
		{"ok", model.NetCheck{Type: "http", Target: srv.URL + "/"}, model.StateOK, "Status 200", 200},
		{"keyword", model.NetCheck{Type: "http", Target: srv.URL + "/", Keyword: "PK95"}, model.StateOK, "Status 200", 200},
		{"keyword missing", model.NetCheck{Type: "http", Target: srv.URL + "/", Keyword: "pk95"}, model.StateCrit, "Suchbegriff „pk95“ nicht gefunden", 200},
		{"keyword after 1 MiB", model.NetCheck{Type: "http", Target: srv.URL + "/big", Keyword: "late"}, model.StateCrit, "nicht gefunden", 200},
		{"server error", model.NetCheck{Type: "http", Target: srv.URL + "/error"}, model.StateCrit, "Status 500", 500},
		{"not found", model.NetCheck{Type: "http", Target: srv.URL + "/missing"}, model.StateCrit, "Status 404 Not Found", 404},
		{"expected 404", model.NetCheck{Type: "http", Target: srv.URL + "/missing", ExpectStatus: 404}, model.StateOK, "Status 404", 404},
		{"status mismatch", model.NetCheck{Type: "http", Target: srv.URL + "/", ExpectStatus: 201}, model.StateCrit, "Status 200 (erwartet 201)", 200},
		{"created", model.NetCheck{Type: "http", Target: srv.URL + "/created", ExpectStatus: 201}, model.StateOK, "Status 201", 201},
		{"redirect", model.NetCheck{Type: "http", Target: srv.URL + "/r1", Keyword: "Willkommen"}, model.StateOK, "Status 200", 200},
		{"redirect loop", model.NetCheck{Type: "http", Target: srv.URL + "/loop"}, model.StateCrit, "mehr als 5 Weiterleitungen", 0},
		{"head", model.NetCheck{Type: "http", Target: srv.URL + "/", Method: "HEAD"}, model.StateOK, "Status 200", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := runCheck(tt.c)
			expectState(t, c, tt.state, tt.msg)
			status, ok := metric(c, "http.status")
			if tt.status == 0 {
				if ok {
					t.Fatalf("unexpected http.status metric %v", status)
				}
				return
			}
			if status != tt.status {
				t.Fatalf("http.status = %v, want %v", status, tt.status)
			}
			if _, ok := metric(c, "http.time"); !ok {
				t.Fatal("http.time missing")
			}
			if _, ok := metric(c, "cert.days"); ok {
				t.Fatal("cert.days on plain http")
			}
			if c.Facts["Statuscode"] != fmt.Sprint(tt.status) {
				t.Fatalf("facts = %v", c.Facts)
			}
			wantMethod := "GET"
			if tt.c.Method != "" {
				wantMethod = tt.c.Method
			}
			mu.Lock()
			defer mu.Unlock()
			if gotMethod != wantMethod || gotUA != userAgent {
				t.Fatalf("request method %q UA %q", gotMethod, gotUA)
			}
		})
	}
}

func TestHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	start := time.Now()
	c := runCheck(model.NetCheck{Type: "http", Target: srv.URL, Timeout: 1})
	expectState(t, c, model.StateCrit, "Zeitüberschreitung nach 1 s")
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestHTTPRefused(t *testing.T) {
	addr := closedPort(t)
	c := runCheck(model.NetCheck{Type: "http", Target: "http://" + addr + "/"})
	expectState(t, c, model.StateCrit, "Verbindung abgelehnt")
}

func TestHTTPS(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secure")
	})

	t.Run("ignore tls", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "http", Target: srv.URL, IgnoreTLS: true, Keyword: "secure"})
		expectState(t, c, model.StateOK, "Status 200")
		days, ok := metric(c, "cert.days")
		if !ok || days < 300 {
			t.Fatalf("cert.days = %v (%v)", days, ok)
		}
		if c.Facts["Aussteller"] == "" || c.Facts["Zertifikat gültig bis"] == "" {
			t.Fatalf("facts = %v", c.Facts)
		}
	})
	t.Run("self-signed", func(t *testing.T) {
		c := runCheck(model.NetCheck{Type: "http", Target: srv.URL})
		expectState(t, c, model.StateCrit, "Zertifikat ungültig: unbekannte Zertifizierungsstelle")
		if _, ok := metric(c, "cert.days"); !ok {
			t.Fatal("cert.days missing for an invalid certificate")
		}
	})
	t.Run("trusted", func(t *testing.T) {
		trust(t, srv)
		c := runCheck(model.NetCheck{Type: "http", Target: srv.URL})
		expectState(t, c, model.StateOK, "Status 200")
	})
}
