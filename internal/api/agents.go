package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// agentBody limits report bodies (a report with many services and interfaces stays far
// below); registerBody limits the registration.
const (
	agentBody    = 8 << 20
	registerBody = 64 << 10
)

// Limits of one agent report. A real agent stays far below; a broken or hostile agent must not
// create unbounded series or store megabytes of text per check. Excess entries are dropped
// (not rejected: the agent would discard the whole report on a 4xx).
const (
	maxReportChecks    = 2000
	maxCheckMetrics    = 64
	maxReportMetrics   = 8000
	maxCheckFacts      = 32
	maxCheckKeyLen     = 200
	maxCheckNameLen    = 200
	maxCheckGroupLen   = 64
	maxCheckMessageLen = 2000
	maxMetricNameLen   = 64
	maxMetricInstLen   = 200
	maxMetricUnitLen   = 16
	maxFactKeyLen      = 64
	maxFactValueLen    = 500
)

// clipText cuts s to at most n runes.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// limitReport applies the report limits in place.
func limitReport(rep *model.AgentReport) {
	if len(rep.Checks) > maxReportChecks {
		rep.Checks = rep.Checks[:maxReportChecks]
	}
	metrics := 0
	for i := range rep.Checks {
		ch := &rep.Checks[i]
		ch.Key = clipText(ch.Key, maxCheckKeyLen)
		ch.Name = clipText(ch.Name, maxCheckNameLen)
		ch.Group = clipText(ch.Group, maxCheckGroupLen)
		ch.Message = clipText(ch.Message, maxCheckMessageLen)
		if len(ch.Metrics) > maxCheckMetrics {
			ch.Metrics = ch.Metrics[:maxCheckMetrics]
		}
		if left := maxReportMetrics - metrics; len(ch.Metrics) > left {
			ch.Metrics = ch.Metrics[:max(left, 0)]
		}
		metrics += len(ch.Metrics)
		for j := range ch.Metrics {
			m := &ch.Metrics[j]
			m.Name = clipText(m.Name, maxMetricNameLen)
			m.Instance = clipText(m.Instance, maxMetricInstLen)
			m.Unit = clipText(m.Unit, maxMetricUnitLen)
		}
		if len(ch.Facts) > 0 {
			facts := make(map[string]string, min(len(ch.Facts), maxCheckFacts))
			for k, v := range ch.Facts {
				if len(facts) >= maxCheckFacts {
					break
				}
				facts[clipText(k, maxFactKeyLen)] = clipText(v, maxFactValueLen)
			}
			ch.Facts = facts
		}
	}
}

func firstValue(h string) string {
	v, _, _ := strings.Cut(h, ",")
	return strings.TrimSpace(v)
}

// validHost accepts host names, IPv4 and bracketed IPv6 addresses with an optional port.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]_", c)) {
			return false
		}
	}
	return true
}

// serverURL is the address agents use to reach this server: the public URL when configured,
// otherwise derived from the request (behind a reverse proxy, i.e. from a loopback or private
// address, from X-Forwarded-Proto and X-Forwarded-Host).
func serverURL(public string, r *http.Request) string {
	if public = strings.TrimRight(strings.TrimSpace(public), "/"); public != "" {
		return public
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	proxied := fromProxy(r)
	if p := strings.ToLower(firstValue(r.Header.Get("X-Forwarded-Proto"))); proxied && (p == "http" || p == "https") {
		scheme = p
	}
	host := ""
	if proxied {
		host = firstValue(r.Header.Get("X-Forwarded-Host"))
	}
	if !validHost(host) {
		host = r.Host
	}
	if !validHost(host) {
		return ""
	}
	return scheme + "://" + host
}

// params fill the install scripts and one-liners. Fingerprint and public key pin are only
// passed for a self-signed certificate; with a CA certificate curl and PowerShell verify it.
func (s *Server) params(r *http.Request) agentdist.Params {
	p := agentdist.Params{ServerURL: serverURL(s.C.PublicURL(), r), Version: s.C.Agents.Version()}
	if s.Opt.Pinned {
		p.Fingerprint, p.PubKeyPin = s.Opt.Fingerprint, s.Opt.PubKeyPin
	}
	return p
}

// agents is the "Agents" page: commands, enrollment key and all agents (attached ones with
// parent_name). With ?device=<id>[&node=<node>] the commands install an agent that attaches
// itself to that device.
func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	bins := s.C.Agents.Binaries()
	p := s.params(r)
	if id := strings.TrimSpace(r.URL.Query().Get("device")); id != "" {
		d, err := s.C.Device(id)
		if err != nil {
			fail(w, err)
			return
		}
		if d.Type == model.TypeAgent {
			writeErr(w, http.StatusBadRequest, "An einen Agent können keine weiteren Agents angehängt werden.")
			return
		}
		p.DeviceID, p.Node = d.ID, strings.TrimSpace(r.URL.Query().Get("node"))
	}
	key := s.C.EnrollKey()
	linux, windows := agentdist.Commands(p, key, bins)
	list := []core.DeviceRow{}
	for _, d := range s.C.AllRows() {
		if d.Type == model.TypeAgent {
			list = append(list, d)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enroll_key": key, "server_url": p.ServerURL, "fingerprint": s.Opt.Fingerprint, "version": s.C.Agents.Version(),
		"auto_update": s.C.Settings().Agent.AutoUpdate, "linux_cmd": linux, "windows_cmd": windows,
		"binaries": bins, "agents": list,
	})
}

func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.C.RotateEnrollKey()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enroll_key": key})
}

// ---- agent endpoints (no session) ----

func (s *Server) installScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := agentdist.InstallScript(kind, s.params(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Fehler: " + err.Error() + "\n"))
			return
		}
		_, _ = w.Write(b)
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	goos, arch := r.PathValue("os"), r.PathValue("arch")
	b, path, found := s.C.Agents.Lookup(goos, arch)
	if !found {
		writeErr(w, http.StatusNotFound, "Die Agent-Datei für "+goos+"/"+arch+" ist auf diesem Server nicht vorhanden.")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Die Agent-Datei für "+goos+"/"+arch+" ist auf diesem Server nicht vorhanden.")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		internalErr(w, "agent download", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+b.File+`"`)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Checksum-SHA256", b.SHA256)
	http.ServeContent(w, r, b.File, st.ModTime(), f)
}

func (s *Server) agentVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.C.Agents.Version(), "binaries": s.C.Agents.Binaries()})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// register enrolls an agent with the enrollment key (401 on a wrong key, rate limited).
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.limited(ip) {
		writeErr(w, http.StatusTooManyRequests, tooMany)
		return
	}
	var req model.AgentRegister
	if !decodeLimit(w, r, &req, registerBody) {
		return
	}
	res, err := s.C.RegisterAgent(r.Context(), req, ip)
	if errors.Is(err, core.ErrBadEnrollKey) {
		s.failed(ip)
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var bad *core.InputError
	if errors.As(err, &bad) {
		writeErr(w, http.StatusBadRequest, bad.Msg)
		return
	}
	if err != nil {
		internalErr(w, "agent register", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// report ingests an agent report (Authorization: Bearer <agent token>). 401 tells the agent
// that its token is gone (device deleted); 5xx makes it buffer the report and retry. The
// token is checked before the body is read, so unauthenticated callers cannot make the server
// parse megabytes of JSON.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "Agent-Token fehlt.")
		return
	}
	if _, err := s.auths.AgentDevice(token); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, core.ErrUnauthorized.Error())
		} else {
			internalErr(w, "agent report: token", err)
		}
		return
	}
	var rep model.AgentReport
	if !decodeLimit(w, r, &rep, agentBody) {
		return
	}
	limitReport(&rep)
	reply, err := s.C.AgentReport(r.Context(), token, rep, s.clientIP(r))
	if errors.Is(err, core.ErrUnauthorized) || errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, core.ErrUnauthorized.Error())
		return
	}
	if err != nil {
		internalErr(w, "agent report", err)
		return
	}
	writeJSON(w, http.StatusOK, reply)
}
