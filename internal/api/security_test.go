package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// TestRouteAccess enumerates every registered route: only the expected ones are public, and
// every other one answers 401 without a session, API token or agent token (the handlers are
// never reached, so the test server has no core).
func TestRouteAccess(t *testing.T) {
	s := testServer()
	s.routes()
	public := map[string]bool{
		"GET /api/session": true, "POST /api/login": true, "POST /api/logout": true, "POST /api/setup": true,
		"GET /agent/install.sh": true, "GET /agent/install.ps1": true, "GET /agent/download/{os}/{arch}": true,
		"GET /agent/version": true, "POST /agent/v1/register": true,
	}
	seen := 0
	for _, rt := range s.routeList {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if rt.access == "public" {
			if !public[rt.pattern] {
				t.Errorf("unexpected public route %s", rt.pattern)
			}
			seen++
			continue
		}
		path = strings.NewReplacer("{id}", "x1", "{os}", "linux", "{arch}", "amd64").Replace(path)
		for _, variant := range []string{"none", "bad cookie", "bad bearer", "csrf header only"} {
			r := httptest.NewRequest(method, path, strings.NewReader("{}"))
			r.RemoteAddr = "203.0.113.5:1234"
			switch variant {
			case "bad cookie":
				r.AddCookie(&http.Cookie{Name: cookieName, Value: "bad"})
			case "bad bearer":
				r.Header.Set("Authorization", "Bearer pk95_bad")
			case "csrf header only":
				r.Header.Set("X-PK95", "1")
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s (%s): %d, want 401", rt.pattern, variant, rec.Code)
			}
		}
		// A session cookie never opens the external API, an API token never the UI API.
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.RemoteAddr = "203.0.113.6:1234"
		r.Header.Set("X-PK95", "1")
		switch rt.access {
		case "token", "agent":
			r.AddCookie(&http.Cookie{Name: cookieName, Value: "good"})
		case "session":
			r.Header.Set("Authorization", "Bearer pk95_good")
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with the wrong kind of credential: %d, want 401", rt.pattern, rec.Code)
		}
		if rt.access == "token" && method != http.MethodGet {
			t.Errorf("external API route %s is not read-only", rt.pattern)
		}
	}
	if seen != len(public) {
		t.Errorf("%d public routes registered, want %d", seen, len(public))
	}
	if len(s.routeList) < 40 {
		t.Errorf("only %d routes recorded", len(s.routeList))
	}
}

func TestForwardedHeadersTrust(t *testing.T) {
	s := testServer()
	req := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/api/agents", nil)
		r.RemoteAddr = remote
		r.Host = "10.0.0.5:8080"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	https := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "mon.kunde.de"}
	if !s.secureCookie(req("127.0.0.1:5000", https)) || !s.secureCookie(req("172.17.0.1:5000", https)) {
		t.Error("Secure missing behind an HTTPS reverse proxy")
	}
	if s.secureCookie(req("203.0.113.9:5000", https)) {
		t.Error("X-Forwarded-Proto trusted from a public address")
	}
	if s.secureCookie(req("127.0.0.1:5000", nil)) {
		t.Error("Secure on plain HTTP without proxy header")
	}
	s.Opt.Secure = true
	if !s.secureCookie(req("203.0.113.9:5000", nil)) {
		t.Error("Secure missing with TLS")
	}
	if got := serverURL("", req("203.0.113.9:5000", https)); got != "http://10.0.0.5:8080" {
		t.Errorf("forwarded headers trusted from a public address: %s", got)
	}
	if got := serverURL("", req("192.168.1.2:5000", https)); got != "https://mon.kunde.de" {
		t.Errorf("proxy: %s", got)
	}
}

func TestLimitReport(t *testing.T) {
	long := strings.Repeat("ä", 5000)
	rep := model.AgentReport{}
	for i := 0; i < maxReportChecks+50; i++ {
		ch := model.Check{Key: fmt.Sprintf("k%d", i), Name: long, Group: long, Message: long, State: "ok"}
		if i < 3 {
			for j := 0; j < 100; j++ {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: long, Instance: long, Unit: long, Value: 1})
			}
			ch.Facts = map[string]string{}
			for j := 0; j < 100; j++ {
				ch.Facts[fmt.Sprintf("%d%s", j, long)] = long
			}
		}
		rep.Checks = append(rep.Checks, ch)
	}
	limitReport(&rep)
	if len(rep.Checks) != maxReportChecks {
		t.Errorf("checks %d", len(rep.Checks))
	}
	c := rep.Checks[0]
	if len([]rune(c.Name)) != maxCheckNameLen || len([]rune(c.Message)) != maxCheckMessageLen || len([]rune(c.Group)) != maxCheckGroupLen {
		t.Errorf("strings not clipped: %d %d %d", len([]rune(c.Name)), len([]rune(c.Message)), len([]rune(c.Group)))
	}
	if len(c.Metrics) != maxCheckMetrics || len([]rune(c.Metrics[0].Name)) != maxMetricNameLen ||
		len([]rune(c.Metrics[0].Instance)) != maxMetricInstLen || len([]rune(c.Metrics[0].Unit)) != maxMetricUnitLen {
		t.Errorf("metrics not limited: %d", len(c.Metrics))
	}
	if len(c.Facts) != maxCheckFacts {
		t.Errorf("facts %d", len(c.Facts))
	}
	for k, v := range c.Facts {
		if len([]rune(k)) > maxFactKeyLen || len([]rune(v)) > maxFactValueLen {
			t.Error("fact not clipped")
		}
	}
	if rep.Checks[5].Key != "k5" {
		t.Errorf("short key changed: %s", rep.Checks[5].Key)
	}

	// The total number of metrics is bounded as well.
	rep = model.AgentReport{}
	for i := 0; i < maxReportChecks; i++ {
		ch := model.Check{Key: fmt.Sprintf("k%d", i)}
		for j := 0; j < maxCheckMetrics; j++ {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "m", Instance: fmt.Sprint(j)})
		}
		rep.Checks = append(rep.Checks, ch)
	}
	limitReport(&rep)
	n := 0
	for _, ch := range rep.Checks {
		n += len(ch.Metrics)
	}
	if n != maxReportMetrics {
		t.Errorf("total metrics %d, want %d", n, maxReportMetrics)
	}
}

func TestFailMapBounded(t *testing.T) {
	s := testServer()
	old := time.Now().Add(-time.Hour)
	for i := 0; i < maxFailIPs; i++ {
		s.fails[fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)] = []time.Time{old}
	}
	s.failed("192.0.2.1")
	if len(s.fails) != 1 {
		t.Errorf("stale entries kept: %d", len(s.fails))
	}
	for i := 0; i < 50; i++ {
		s.failed("192.0.2.1")
	}
	if n := len(s.fails["192.0.2.1"]); n > 20 || !s.limited("192.0.2.1") {
		t.Errorf("history %d", n)
	}
}

func TestInternalErrHidesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	internalErr(rec, "test", fmt.Errorf("open /data/secret.db: permission denied"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "/data") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestBadJSONMessage(t *testing.T) {
	var v struct {
		Checks []model.Check `json:"checks"`
	}
	for body, want := range map[string]string{
		`{"checks":"x"}`: "Ungültige Anfrage: Feld „checks“ hat den falschen Typ.",
		`{"checks":`:     "Ungültige Anfrage.", // unexpected EOF
		`{x}`:            "Ungültige Anfrage: kein gültiges JSON.",
	} {
		rec := httptest.NewRecorder()
		decode(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)), &v)
		if got := errorOf(t, rec); got != want || strings.Contains(got, "model.") {
			t.Errorf("%s: %q", body, got)
		}
	}
}
