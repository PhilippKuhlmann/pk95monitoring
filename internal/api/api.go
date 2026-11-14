// Package api serves the JSON API of the web UI, the agent endpoints, the external API
// (bearer token, read-only) and the embedded web UI.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/store"
)

const (
	cookieName = "pk95mon_session"
	sessionTTL = 7 * 24 * time.Hour
	csp        = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"
)

// Options describe how the server is reached.
type Options struct {
	Secure      bool   // served over HTTPS (session cookie with Secure)
	Fingerprint string // SHA-256 of the server certificate as AA:BB:…, shown in the UI ("" without TLS)
	PubKeyPin   string // base64 SHA-256 of the certificate's public key (curl --pinnedpubkey)
	// Pinned: the certificate is self-signed (not trusted by the system CAs), so the agent
	// commands pin it by fingerprint and public key.
	Pinned bool
}

// authStore is the part of the store the auth middlewares need (stubbed in tests).
type authStore interface {
	SessionUser(token string) (store.User, error)
	CheckAPIToken(plain string) (store.APIToken, error)
	AgentDevice(token string) (string, error)
}

// Server is the HTTP handler.
type Server struct {
	C   *core.Core
	Web fs.FS
	Opt Options

	auths authStore
	mux   *http.ServeMux

	limMu sync.Mutex
	fails map[string][]time.Time // failed logins, setup codes and enrollment keys per IP
	hits  map[string]*window     // external API requests per IP

	setupMu sync.Mutex // serializes POST /api/setup, so two requests cannot both create a first user

	// routeList records every route with its access class (see routes); used by the tests to
	// check that no route is reachable without authentication by accident.
	routeList []route
}

// route is one registered pattern and how it is protected.
type route struct {
	pattern string
	access  string // "public", "session", "token" (external API), "agent"
}

// New builds the handler.
func New(c *core.Core, web fs.FS, opt Options) *Server {
	s := &Server{C: c, Web: web, Opt: opt, auths: c.St, mux: http.NewServeMux(),
		fails: map[string][]time.Time{}, hits: map[string]*window{}}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, r)
	s.mux.ServeHTTP(w, r)
}

func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Content-Security-Policy", csp)
	}
}

func (s *Server) routes() {
	m := s.mux
	pub := func(pattern string, h http.HandlerFunc) {
		s.routeList = append(s.routeList, route{pattern, "public"})
		m.HandleFunc(pattern, h)
	}
	// public
	pub("GET /api/session", s.getSession)
	pub("POST /api/login", s.login)
	pub("POST /api/logout", s.logout)
	pub("POST /api/setup", s.setup)

	a := func(pattern string, h http.HandlerFunc) {
		s.routeList = append(s.routeList, route{pattern, "session"})
		m.Handle(pattern, s.auth(h))
	}
	a("GET /api/overview", s.overview)
	a("GET /api/devices", s.listDevices)
	a("POST /api/devices", s.saveDevice)
	a("POST /api/devices/group", s.setGroup)
	a("GET /api/devices/{id}", s.getDevice)
	a("PUT /api/devices/{id}", s.saveDevice)
	a("DELETE /api/devices/{id}", s.deleteDevice)
	a("POST /api/devices/{id}/poll", s.pollDevice)
	a("POST /api/devices/{id}/maintenance", s.maintenance)
	a("POST /api/devices/{id}/discovery", s.discovery)
	a("POST /api/devices/{id}/attach", s.attach)
	a("POST /api/devices/{id}/detach", s.detach)
	a("GET /api/devices/{id}/series", s.series)
	a("GET /api/devices/{id}/metrics", s.metrics)
	a("GET /api/devices/{id}/sparks", s.sparks)
	a("GET /api/updates", s.updates)
	a("GET /api/expiry", s.expiry)
	a("GET /api/maintenance", s.listWindows)
	a("POST /api/maintenance", s.saveWindow)
	a("PUT /api/maintenance/{id}", s.saveWindow)
	a("DELETE /api/maintenance/{id}", s.deleteWindow)
	a("POST /api/probe", s.probe)
	a("POST /api/test/proxmox", s.testProxmox)
	a("POST /api/test/snmp", s.testSNMP)
	a("POST /api/test/nut", s.testNUT)
	a("POST /api/test/netcheck", s.testNetCheck)
	a("GET /api/alerts", s.alerts)
	a("POST /api/alerts/{id}/ack", s.ackAlert)
	a("POST /api/alerts/{id}/unack", s.unackAlert)
	a("GET /api/report", s.availability)
	a("GET /api/report/mail", s.reportMailPreview)
	a("POST /api/report/send", s.sendReport)
	a("GET /api/events", s.events)
	a("GET /api/agents", s.agents)
	a("POST /api/agents/key", s.rotateKey)
	a("GET /api/settings", s.getSettings)
	a("PUT /api/settings", s.putSettings)
	a("POST /api/settings/test", s.testNotify)
	a("POST /api/settings/telegram/chats", s.telegramChats)
	a("GET /api/tokens", s.listTokens)
	a("POST /api/tokens", s.createToken)
	a("DELETE /api/tokens/{id}", s.deleteToken)
	a("POST /api/account/password", s.changePassword)
	a("GET /api/system", s.system)

	// agents (no session): scripts and binaries are public, registration needs the enrollment
	// key, the report the agent token.
	pub("GET /agent/install.sh", s.installScript("sh"))
	pub("GET /agent/install.ps1", s.installScript("ps1"))
	pub("GET /agent/download/{os}/{arch}", s.download)
	pub("GET /agent/version", s.agentVersion)
	pub("POST /agent/v1/register", s.register)
	s.routeList = append(s.routeList, route{"POST /agent/v1/report", "agent"})
	m.HandleFunc("POST /agent/v1/report", s.report)

	// external API (API token, read-only)
	x := func(pattern string, h http.HandlerFunc) {
		s.routeList = append(s.routeList, route{pattern, "token"})
		m.Handle(pattern, s.apiToken(h))
	}
	x("GET /api/v1/summary", s.v1Summary)
	x("GET /api/v1/devices", s.v1Devices)
	x("GET /api/v1/devices/{id}", s.v1Device)
	x("GET /api/v1/alerts", s.v1Alerts)
	x("GET /api/v1/report", s.availability)

	m.Handle("/", s.static())
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// fieldErrs answers 422 with the field errors; it returns false when there are none.
func fieldErrs(w http.ResponseWriter, errs map[string]string) bool {
	if len(errs) == 0 {
		return false
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Bitte die markierten Felder prüfen.", "fields": errs})
	return true
}

func fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "Nicht gefunden.")
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

// internalErr logs an unexpected error and answers 500 without its details (they may name
// files, SQL or other internals).
func internalErr(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	writeErr(w, http.StatusInternalServerError, "Interner Fehler. Details stehen im Protokoll des Servers.")
}

const maxBody = 1 << 20

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, maxBody)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "Anfrage zu groß.")
			return false
		}
		writeErr(w, http.StatusBadRequest, badJSON(err))
		return false
	}
	return true
}

// badJSON describes a JSON decoding error without Go type names.
func badJSON(err error) string {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &te) && te.Field != "":
		return "Ungültige Anfrage: Feld „" + te.Field + "“ hat den falschen Typ."
	case errors.As(err, &se):
		return "Ungültige Anfrage: kein gültiges JSON."
	}
	return "Ungültige Anfrage."
}

func ok(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// clientIP is the address of the caller. Without TLS the server runs behind a reverse proxy:
// when the request comes from a loopback or private address, the last address in
// X-Forwarded-For is the client (so the rate limits do not lock out everybody).
func (s *Server) clientIP(r *http.Request) string {
	ip := remoteIP(r)
	xff := r.Header.Get("X-Forwarded-For")
	if s.Opt.Secure || xff == "" || !fromProxy(r) {
		return ip
	}
	parts := strings.Split(xff, ",")
	if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
		return last
	}
	return ip
}

// fromProxy reports whether the request comes from a loopback or private address, i.e.
// possibly from a reverse proxy in front of the server. Only then are the X-Forwarded-*
// headers taken into account; a client on the internet cannot set them for us.
func fromProxy(r *http.Request) bool {
	p := net.ParseIP(remoteIP(r))
	return p != nil && (p.IsLoopback() || p.IsPrivate())
}

// ---- auth ----

type ctxKey struct{}

// auth requires a session; mutating requests also need the header X-PK95: 1 (a custom header
// forces a CORS preflight, so other sites cannot send them).
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Bitte anmelden.")
			return
		}
		u, err := s.auths.SessionUser(c.Value)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Sitzung abgelaufen, bitte neu anmelden.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-PK95") != "1" {
			writeErr(w, http.StatusForbidden, "Anfrage abgelehnt.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func currentUser(r *http.Request) store.User {
	u, _ := r.Context().Value(ctxKey{}).(store.User)
	return u
}

// SetupCodePath is the file holding the one-time setup code.
func SetupCodePath(dataDir string) string { return filepath.Join(dataDir, "setup-code") }

func (s *Server) setupCode() string {
	b, err := os.ReadFile(SetupCodePath(s.C.Opt.DataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	n, _ := s.C.St.UserCount()
	resp := map[string]any{"setup": n == 0, "version": s.C.Opt.Version, "instance": s.C.InstanceName()}
	if c, err := r.Cookie(cookieName); err == nil {
		if u, err := s.auths.SessionUser(c.Value); err == nil {
			resp["user"] = u.Username
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// limited reports whether ip had 10 failures in the last 15 minutes.
func (s *Server) limited(ip string) bool {
	s.limMu.Lock()
	defer s.limMu.Unlock()
	var keep []time.Time
	for _, t := range s.fails[ip] {
		if time.Since(t) < 15*time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(s.fails, ip)
	} else {
		s.fails[ip] = keep
	}
	return len(keep) >= 10
}

// maxFailIPs bounds the failure map: beyond it, addresses without a recent failure are dropped
// (otherwise every address that ever failed once would stay in memory).
const maxFailIPs = 10000

func (s *Server) failed(ip string) {
	s.limMu.Lock()
	defer s.limMu.Unlock()
	now := time.Now()
	if len(s.fails) >= maxFailIPs {
		for k, list := range s.fails {
			if len(list) == 0 || now.Sub(list[len(list)-1]) >= 15*time.Minute {
				delete(s.fails, k)
			}
		}
	}
	list := append(s.fails[ip], now)
	if len(list) > 20 { // the limit only looks at the last 10
		list = list[len(list)-20:]
	}
	s.fails[ip] = list
}

const tooMany = "Zu viele Fehlversuche. Bitte 15 Minuten warten."

// secureCookie: HTTPS directly, or plain HTTP from a reverse proxy that terminated HTTPS
// (X-Forwarded-Proto: https, only trusted from a loopback or private address).
func (s *Server) secureCookie(r *http.Request) bool {
	return s.Opt.Secure || (fromProxy(r) && strings.EqualFold(firstValue(r.Header.Get("X-Forwarded-Proto")), "https"))
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	tok := randomToken(32)
	if err := s.C.St.CreateSession(tok, userID, sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.secureCookie(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.limited(ip) {
		writeErr(w, http.StatusTooManyRequests, tooMany)
		return
	}
	var req struct{ Username, Password string }
	if !decode(w, r, &req) {
		return
	}
	u, err := s.C.St.UserByName(strings.TrimSpace(req.Username))
	hash := u.Hash
	if err != nil {
		hash = dummyHash()
	}
	if !checkPassword(hash, req.Password) || err != nil {
		s.failed(ip)
		time.Sleep(400 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "Benutzername oder Passwort falsch.")
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.C.St.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode})
	ok(w)
}

func validPassword(pw string) string {
	if len([]rune(pw)) < 10 {
		return "Mindestens 10 Zeichen."
	}
	if len(pw) > 256 {
		return "Höchstens 256 Zeichen."
	}
	return ""
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.limited(ip) {
		writeErr(w, http.StatusTooManyRequests, tooMany)
		return
	}
	var req struct{ Code, Username, Password string }
	if !decode(w, r, &req) {
		return
	}
	// One setup at a time: the user count check and CreateUser must not interleave.
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	n, err := s.C.St.UserCount()
	if err != nil {
		internalErr(w, "setup: user count", err)
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "Die Einrichtung ist bereits abgeschlossen.")
		return
	}
	code := s.setupCode()
	if code == "" || subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(req.Code))), []byte(code)) != 1 {
		s.failed(ip)
		fieldErrs(w, map[string]string{"code": "Einrichtungscode stimmt nicht. Er steht im Protokoll des Containers (docker logs) und in der Datei setup-code im Datenverzeichnis."})
		return
	}
	errs := map[string]string{}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Username) > 64 || strings.ContainsAny(req.Username, " \t") {
		errs["username"] = "3 bis 64 Zeichen, ohne Leerzeichen."
	}
	if m := validPassword(req.Password); m != "" {
		errs["password"] = m
	}
	if fieldErrs(w, errs) {
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := s.C.St.CreateUser(req.Username, hash)
	if err != nil {
		fail(w, err)
		return
	}
	_ = os.Remove(SetupCodePath(s.C.Opt.DataDir))
	_ = s.startSession(w, r, id)
	s.C.Event("info", "System", "Einrichtung abgeschlossen", "Konto „"+req.Username+"“ angelegt.", nil, false)
	writeJSON(w, http.StatusOK, map[string]any{"user": req.Username})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !decode(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if s.limited(ip) {
		writeErr(w, http.StatusTooManyRequests, tooMany)
		return
	}
	u := currentUser(r)
	if !checkPassword(u.Hash, req.Old) {
		s.failed(ip)
		fieldErrs(w, map[string]string{"old": "Aktuelles Passwort stimmt nicht."})
		return
	}
	if m := validPassword(req.New); m != "" {
		fieldErrs(w, map[string]string{"new": m})
		return
	}
	hash, err := hashPassword(req.New)
	if err != nil {
		fail(w, err)
		return
	}
	c, _ := r.Cookie(cookieName)
	if err := s.C.St.SetPassword(u.ID, hash, c.Value); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// ---- static ----

// static serves the web UI. Page paths (no dot, e.g. /devices/abc, or anything below /devices/,
// whose check pages /devices/<id>/checks/<key> may carry keys like svc:ssh.service) get
// index.html; fonts are cached for a year, everything else is revalidated.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/agent/") {
			writeErr(w, http.StatusNotFound, "Unbekannter Pfad.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeErr(w, http.StatusMethodNotAllowed, "Methode nicht erlaubt.")
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || !strings.Contains(p, ".") || strings.HasPrefix(p, "devices/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
