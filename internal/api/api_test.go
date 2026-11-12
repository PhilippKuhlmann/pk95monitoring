package api

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

func TestMain(m *testing.M) {
	hashIter = 1000 // keep the tests fast
	os.Exit(m.Run())
}

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRedactDevice(t *testing.T) {
	d := model.Device{ID: "d1", Name: "pve", Type: model.TypeProxmox, Host: "10.0.0.2", Enabled: true,
		Proxmox: &model.ProxmoxConfig{Port: 8006, TokenID: "root@pam!mon", Secret: "s3cret", Fingerprint: "AA:BB", IgnoreGuests: []string{"100"}}}
	m := toMap(t, redactDevice(d))
	px := m["proxmox"].(map[string]any)
	if px["secret"] != "" || px["has_secret"] != true || px["token_id"] != "root@pam!mon" || px["port"] != 8006.0 || px["fingerprint"] != "AA:BB" {
		t.Errorf("proxmox %v", px)
	}
	if m["name"] != "pve" || m["host"] != "10.0.0.2" || m["enabled"] != true || m["id"] != "d1" {
		t.Errorf("device fields lost: %v", m)
	}
	if _, ok := m["snmp"]; ok {
		t.Error("snmp key without snmp config")
	}
	if c, ok := m["checks"].([]any); !ok || len(c) != 0 {
		t.Errorf("checks %v", m["checks"])
	}
	if d.Proxmox.Secret != "s3cret" {
		t.Error("redaction changed the original")
	}

	sn := model.Device{ID: "s", Type: model.TypeSNMP, SNMP: &model.SNMPConfig{Version: "3", User: "mon", AuthPass: "a", PrivPass: "p", Interfaces: []string{"ge-1"}}}
	b, _ := json.Marshal(redactDevice(sn))
	for _, secret := range []string{`"a"`, `"p"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("secret %s in %s", secret, b)
		}
	}
	s := toMap(t, redactDevice(sn))["snmp"].(map[string]any)
	if s["community"] != "" || s["has_community"] != false || s["has_auth_pass"] != true || s["has_priv_pass"] != true ||
		s["auth_pass"] != "" || s["priv_pass"] != "" || s["user"] != "mon" || s["version"] != "3" {
		t.Errorf("snmp %v", s)
	}
	if ifs := s["interfaces"].([]any); len(ifs) != 1 {
		t.Errorf("interfaces %v", ifs)
	}
	if _, ok := toMap(t, redactDevice(sn))["proxmox"]; ok {
		t.Error("proxmox key without proxmox config")
	}
}

func TestRedactSettings(t *testing.T) {
	s := core.Settings{
		General: core.GeneralSettings{Instance: "Muster", RetentionRawDays: 2},
		Notify:  notify.Settings{MailEnabled: true, SMTPHost: "mail", SMTPPass: "pw", TelegramToken: "1:AA", TelegramChat: "42"},
		Central: core.CentralSettings{Enabled: true, URL: "https://central", Token: "tok", Interval: 5},
		Agent:   core.AgentSettings{AutoUpdate: true},
	}
	b, _ := json.Marshal(redactSettings(s))
	for _, secret := range []string{`"pw"`, `"1:AA"`, `"tok"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("secret %s in %s", secret, b)
		}
	}
	m := toMap(t, redactSettings(s))
	n := m["notify"].(map[string]any)
	if n["smtp_password"] != "" || n["has_smtp_password"] != true || n["has_hook_token"] != false || n["has_telegram_token"] != true ||
		n["telegram_token"] != "" || n["smtp_host"] != "mail" || n["telegram_chat"] != "42" || n["mail_enabled"] != true {
		t.Errorf("notify %v", n)
	}
	c := m["central"].(map[string]any)
	if _, ok := c["token"]; ok || c["has_token"] != true || c["url"] != "https://central" || c["interval"] != 5.0 {
		t.Errorf("central %v", c)
	}
	if th, ok := m["thresholds"].([]any); !ok || len(th) != 0 {
		t.Errorf("thresholds %v", m["thresholds"])
	}
	if d := m["defaults"].([]any); len(d) != len(model.DefaultThresholds()) {
		t.Errorf("defaults %v", d)
	}
	if m["general"].(map[string]any)["instance"] != "Muster" || m["agent"].(map[string]any)["auto_update"] != true {
		t.Errorf("sections %v", m)
	}
}

func TestMergeSettings(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cur := core.Settings{
		General:    core.GeneralSettings{Instance: "A", PublicURL: "https://a", RetentionRawDays: 2, RenotifyHours: 4},
		Notify:     notify.Settings{MinSeverity: "warning", SMTPHost: "mail", SMTPPass: "pw", TelegramFiles: true},
		Thresholds: []model.Threshold{{Metric: "cpu.usage", Warn: f(80)}},
		Central:    core.CentralSettings{URL: "https://c", Token: "tok", Interval: 5},
		Agent:      core.AgentSettings{AutoUpdate: true},
	}
	merge := func(body string) core.Settings {
		t.Helper()
		got, err := mergeSettings(cur, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return got
	}

	// One section per call: the others stay.
	got := merge(`{"general":{"instance":"B","public_url":"","retention_raw_days":3,"retention_5m_days":35,"retention_1h_days":400,"renotify_hours":0}}`)
	if got.General.Instance != "B" || got.General.PublicURL != "" || got.General.RetentionRawDays != 3 || got.General.RenotifyHours != 0 {
		t.Errorf("general %+v", got.General)
	}
	if got.Notify.SMTPPass != "pw" || len(got.Thresholds) != 1 || got.Central.Token != "tok" || !got.Agent.AutoUpdate {
		t.Errorf("other sections changed: %+v", got)
	}

	// Keys missing inside a section keep their value; an empty secret is passed on as "".
	got = merge(`{"notify":{"min_severity":"critical","smtp_password":""}}`)
	if got.Notify.MinSeverity != "critical" || got.Notify.SMTPHost != "mail" || !got.Notify.TelegramFiles || got.Notify.SMTPPass != "" {
		t.Errorf("notify %+v", got.Notify)
	}

	// thresholds: [] resets, null or missing keeps.
	if got := merge(`{"thresholds":[]}`); got.Thresholds == nil || len(got.Thresholds) != 0 {
		t.Errorf("reset %v", got.Thresholds)
	}
	if got := merge(`{"thresholds":null}`); len(got.Thresholds) != 1 {
		t.Errorf("null %v", got.Thresholds)
	}
	got = merge(`{"thresholds":[{"metric":"disk.usage:/var","warn":70,"crit":80,"count":2}]}`)
	if len(got.Thresholds) != 1 || got.Thresholds[0].Metric != "disk.usage:/var" || *got.Thresholds[0].Crit != 80 {
		t.Errorf("replace %+v", got.Thresholds)
	}

	got = merge(`{"central":{"enabled":true,"url":"https://d","token":"","interval":10},"agent":{"auto_update":false}}`)
	if !got.Central.Enabled || got.Central.URL != "https://d" || got.Central.Token != "" || got.Central.Interval != 10 || got.Agent.AutoUpdate {
		t.Errorf("central/agent %+v %+v", got.Central, got.Agent)
	}
	if got := merge(`{}`); got.General != cur.General || got.Notify != cur.Notify {
		t.Error("empty body changed settings")
	}
	if cur.Thresholds[0].Metric != "cpu.usage" || cur.Notify.SMTPPass != "pw" {
		t.Error("merge changed the original")
	}
	for _, bad := range []string{`[]`, `{"general":{"retention_raw_days":"x"}}`, `{"thresholds":{}}`, `not json`} {
		if _, err := mergeSettings(cur, []byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestServerURL(t *testing.T) {
	req := func(host string, tlsOn bool, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/api/agents", nil)
		r.RemoteAddr = "127.0.0.1:40000" // reverse proxy on the same host
		r.Host = host
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, tc := range []struct {
		name, public string
		r            *http.Request
		want         string
	}{
		{"public wins", "https://mon.example.com:8443/", req("10.0.0.1:8443", true, nil), "https://mon.example.com:8443"},
		{"tls", "", req("10.0.0.1:8443", true, nil), "https://10.0.0.1:8443"},
		{"plain", "", req("localhost:18096", false, nil), "http://localhost:18096"},
		{"proxy", "", req("127.0.0.1:8080", false, map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "mon.kunde.de"}), "https://mon.kunde.de"},
		{"proxy lists", "", req("x", false, map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "a.example, b.example"}), "https://a.example"},
		{"ipv6", "", req("[fd00::1]:8443", true, nil), "https://[fd00::1]:8443"},
		{"bad forwarded host", "", req("10.0.0.1", true, map[string]string{"X-Forwarded-Host": "evil'$(reboot)"}), "https://10.0.0.1"},
		{"bad proto", "", req("10.0.0.1", true, map[string]string{"X-Forwarded-Proto": "javascript"}), "https://10.0.0.1"},
		{"bad host", "", req("a b", false, nil), ""},
	} {
		if got := serverURL(tc.public, tc.r); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// stubAuth is a session and API token store for the middleware tests.
type stubAuth struct{}

func (stubAuth) SessionUser(token string) (store.User, error) {
	if token == "good" {
		return store.User{ID: 1, Username: "admin"}, nil
	}
	return store.User{}, store.ErrNotFound
}

func (stubAuth) AgentDevice(token string) (string, error) {
	if token == "agent-good" {
		return "dev1", nil
	}
	return "", store.ErrNotFound
}

func (stubAuth) CheckAPIToken(plain string) (store.APIToken, error) {
	if plain == "pk95_good" {
		return store.APIToken{ID: 1, Name: "Zentrale"}, nil
	}
	return store.APIToken{}, store.ErrNotFound
}

func testServer() *Server {
	return &Server{auths: stubAuth{}, mux: http.NewServeMux(), fails: map[string][]time.Time{}, hits: map[string]*window{}}
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("no JSON error: %q", rec.Body.String())
	}
	return e.Error
}

func TestAuthAndCSRF(t *testing.T) {
	s := testServer()
	var gotUser string
	h := s.auth(func(w http.ResponseWriter, r *http.Request) {
		gotUser = currentUser(r).Username
		ok(w)
	})
	for _, tc := range []struct {
		name, method, cookie string
		csrf                 bool
		want                 int
	}{
		{"no cookie", "GET", "", false, 401},
		{"bad session", "GET", "bad", false, 401},
		{"get", "GET", "good", false, 200},
		{"post without header", "POST", "good", false, 403},
		{"put without header", "PUT", "good", false, 403},
		{"delete without header", "DELETE", "good", false, 403},
		{"post with header", "POST", "good", true, 200},
		{"post with header, no session", "POST", "", true, 401},
	} {
		gotUser = ""
		r := httptest.NewRequest(tc.method, "/api/x", strings.NewReader("{}"))
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: tc.cookie})
		}
		if tc.csrf {
			r.Header.Set("X-PK95", "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
		if tc.want == 200 && gotUser != "admin" {
			t.Errorf("%s: user %q", tc.name, gotUser)
		}
		if tc.want != 200 && (gotUser != "" || errorOf(t, rec) == "") {
			t.Errorf("%s: handler ran or no error message", tc.name)
		}
	}
	r := httptest.NewRequest("POST", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "good"})
	r.Header.Set("X-PK95", "yes")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Errorf("X-PK95 must be exactly 1: %d", rec.Code)
	}
}

func TestExternalAPIAuthAndRate(t *testing.T) {
	s := testServer()
	h := s.apiToken(func(w http.ResponseWriter, r *http.Request) { ok(w) })
	call := func(auth, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/summary", nil)
		r.RemoteAddr = ip + ":5000"
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := call("", "10.0.0.1"); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" || errorOf(t, rec) == "" {
		t.Errorf("missing token: %d", rec.Code)
	}
	if rec := call("Bearer pk95_bad", "10.0.0.1"); rec.Code != 401 {
		t.Errorf("bad token: %d", rec.Code)
	}
	if rec := call("Basic pk95_good", "10.0.0.1"); rec.Code != 401 {
		t.Errorf("basic auth accepted: %d", rec.Code)
	}
	if rec := call("bearer pk95_good", "10.0.0.1"); rec.Code != 200 {
		t.Errorf("good token: %d", rec.Code)
	}
	for i := 4; i < apiRate; i++ {
		call("Bearer pk95_good", "10.0.0.1")
	}
	if rec := call("Bearer pk95_good", "10.0.0.1"); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("request %d: %d, want 429", apiRate+1, rec.Code)
	}
	if rec := call("Bearer pk95_good", "10.0.0.2"); rec.Code != 200 {
		t.Errorf("other address limited: %d", rec.Code)
	}
	// A new window starts after a minute.
	if !s.allow("10.0.0.1", time.Now().Add(61*time.Second)) {
		t.Error("window did not reset")
	}
}

func TestStaticSPA(t *testing.T) {
	s := testServer()
	s.Web = fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html>INDEX")},
		"app.js":           {Data: []byte("console.log(1)")},
		"fonts/sg.woff2":   {Data: []byte("font")},
		"logo.svg":         {Data: []byte("<svg/>")},
		"theme.js":         {Data: []byte("")},
		"style.css":        {Data: []byte("")},
		"fonts/readme.txt": {Data: []byte("x")},
	}
	s.mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) { ok(w) })
	s.mux.Handle("/", s.static())
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	for _, path := range []string{"/", "/devices", "/devices/abc", "/alerts", "/settings", "/events/",
		"/devices/abc/checks/cpu", "/devices/abc/checks/svc%3Assh.service", "/devices/abc/checks/if%3Aeth0.100",
		"/devices/abc/checks/disk%3A%2F", "/devices/abc/checks/snmp%3Aif%3AGi1%2F0%2F1", "/devices/abc/checks/disk%3AC%3A"} {
		rec := get("GET", path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "INDEX") || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q cache=%q", path, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("Content-Security-Policy") != csp || rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: security headers %v", path, rec.Header())
		}
	}
	if rec := get("GET", "/app.js"); rec.Code != 200 || rec.Body.String() != "console.log(1)" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("app.js: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("GET", "/fonts/sg.woff2"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("font: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("GET", "/missing.js"); rec.Code != 404 {
		t.Errorf("missing file: %d", rec.Code)
	}
	for _, path := range []string{"/api/unknown", "/api/v2/x", "/agent/unknown"} {
		rec := get("GET", path)
		if rec.Code != 404 || errorOf(t, rec) == "" {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if rec := get("POST", "/devices"); rec.Code != 405 {
		t.Errorf("POST page: %d", rec.Code)
	}
	if rec := get("GET", "/api/session"); rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("api headers: %v", rec.Header())
	}
}

func TestPassword(t *testing.T) {
	h, err := hashPassword("korrekt-pferd-batterie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$1000$") {
		t.Errorf("hash %s", h)
	}
	if !checkPassword(h, "korrekt-pferd-batterie") || checkPassword(h, "korrekt-pferd-batteriE") || checkPassword(h, "") {
		t.Error("check")
	}
	h2, _ := hashPassword("korrekt-pferd-batterie")
	if h == h2 {
		t.Error("no salt")
	}
	for _, bad := range []string{"", "x", "$2a$12$abc", "pbkdf2-sha256$0$AA$AA", "pbkdf2-sha256$1000$!!$AA", "pbkdf2-sha256$99999999999$AA$AA"} {
		if checkPassword(bad, "x") {
			t.Errorf("accepted hash %q", bad)
		}
	}
	if !checkPassword(dummyHash(), "pk95-dummy-password") {
		t.Error("dummy hash")
	}
	if validPassword("kurz") == "" || validPassword("lang genug!") != "" || validPassword(strings.Repeat("x", 300)) == "" {
		t.Error("validPassword")
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := testServer()
	for i := 0; i < 10; i++ {
		if s.limited("1.2.3.4") {
			t.Fatalf("limited after %d failures", i)
		}
		s.failed("1.2.3.4")
	}
	if !s.limited("1.2.3.4") || s.limited("1.2.3.5") {
		t.Error("limit")
	}
}

func TestClientIP(t *testing.T) {
	r := func(remote, xff string) *http.Request {
		q := httptest.NewRequest("GET", "/", nil)
		q.RemoteAddr = remote
		if xff != "" {
			q.Header.Set("X-Forwarded-For", xff)
		}
		return q
	}
	tlsSrv := &Server{Opt: Options{Secure: true}}
	proxied := &Server{}
	for _, tc := range []struct {
		name string
		s    *Server
		r    *http.Request
		want string
	}{
		{"direct", tlsSrv, r("203.0.113.9:1234", ""), "203.0.113.9"},
		{"tls ignores xff", tlsSrv, r("10.0.0.1:1234", "198.51.100.7"), "10.0.0.1"},
		{"proxy", proxied, r("172.17.0.1:1234", "203.0.113.1, 198.51.100.7"), "198.51.100.7"},
		{"public peer ignores xff", proxied, r("203.0.113.9:1234", "198.51.100.7"), "203.0.113.9"},
		{"garbage xff", proxied, r("127.0.0.1:1234", "nonsense"), "127.0.0.1"},
	} {
		if got := tc.s.clientIP(tc.r); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestHelpers(t *testing.T) {
	if got := metricStep(60, 300); got != 300 {
		t.Errorf("step %d", got)
	}
	if got := metricStep(60, 30); got != 60 {
		t.Errorf("step %d", got)
	}
	if got := metricStep(1800, 90); got != 1800 {
		t.Errorf("step %d", got)
	}
	if got := metricStep(60, 90); got != 120 {
		t.Errorf("step %d", got)
	}
	ids, err := parseIDs("3, 1,3,,2")
	if err != nil || len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("ids %v %v", ids, err)
	}
	for _, bad := range []string{"1,x", "-1", "0"} {
		if _, err := parseIDs(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if intParam("", 50, 1, 500) != 50 || intParam("9999", 50, 1, 500) != 500 || intParam("0", 50, 1, 500) != 1 || intParam("x", 7, 1, 9) != 7 {
		t.Error("intParam")
	}
	r := httptest.NewRequest("GET", "/", nil)
	for h, want := range map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "Bearer": "", "": ""} {
		r.Header.Set("Authorization", h)
		if got := bearer(r); got != want {
			t.Errorf("bearer(%q) = %q", h, got)
		}
	}
	if !validHost("mon.example.com:8443") || !validHost("[fd00::1]:8443") || validHost("a/b") || validHost("") {
		t.Error("validHost")
	}
}

func TestDecodeLimit(t *testing.T) {
	var v map[string]any
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 200)+`"}`))
	if decodeLimit(rec, r, &v, 100) || rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	if decode(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{`)), &v) || rec.Code != 400 {
		t.Errorf("broken body: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	if !decode(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`)), &v) || v["a"] != 1.0 {
		t.Error("good body")
	}
}

func TestFieldErrsAndFail(t *testing.T) {
	rec := httptest.NewRecorder()
	if fieldErrs(rec, nil) || rec.Body.Len() != 0 {
		t.Error("no errors must not answer")
	}
	rec = httptest.NewRecorder()
	if !fieldErrs(rec, map[string]string{"name": "Pflichtfeld."}) || rec.Code != 422 {
		t.Errorf("code %d", rec.Code)
	}
	var e struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error == "" || e.Fields["name"] != "Pflichtfeld." {
		t.Errorf("%+v", e)
	}
	rec = httptest.NewRecorder()
	fail(rec, store.ErrNotFound)
	if rec.Code != 404 {
		t.Errorf("not found: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	fail(rec, errors.New("kaputt"))
	if rec.Code != 502 || errorOf(t, rec) != "kaputt" {
		t.Errorf("error: %d", rec.Code)
	}
}

// TestReportStatusCodes checks the answers the agent relies on before the core is reached.
func TestReportStatusCodes(t *testing.T) {
	s := testServer()
	rec := httptest.NewRecorder()
	s.report(rec, httptest.NewRequest("POST", "/agent/v1/report", strings.NewReader("{}")))
	if rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	// A wrong token is rejected before the body is read.
	rec = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/agent/v1/report", io.LimitReader(&infinite{}, agentBody+10))
	r.Header.Set("Authorization", "Bearer x")
	s.report(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/agent/v1/report", io.LimitReader(&infinite{}, agentBody+10))
	r.Header.Set("Authorization", "Bearer agent-good")
	s.report(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized report: %d", rec.Code)
	}
}

// infinite is an endless JSON string: `"xxxx…`.
type infinite struct{ started bool }

func (f *infinite) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	if !f.started && len(p) > 0 {
		p[0], f.started = '"', true
	}
	return len(p), nil
}
