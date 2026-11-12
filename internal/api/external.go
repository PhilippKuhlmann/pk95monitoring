package api

import (
	"net/http"
	"strconv"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// External API v1: read-only, authenticated by an API token (Authorization: Bearer pk95_…),
// limited to apiRate requests per minute and client address.

const apiRate = 60

type window struct {
	start time.Time
	n     int
}

// allow counts a request of ip in the current one-minute window.
func (s *Server) allow(ip string, now time.Time) bool {
	s.limMu.Lock()
	defer s.limMu.Unlock()
	if len(s.hits) > 10000 {
		for k, v := range s.hits {
			if now.Sub(v.start) >= time.Minute {
				delete(s.hits, k)
			}
		}
	}
	h := s.hits[ip]
	if h == nil || now.Sub(h.start) >= time.Minute {
		h = &window{start: now}
		s.hits[ip] = h
	}
	h.n++
	return h.n <= apiRate
}

func (s *Server) apiToken(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(s.clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "Zu viele Anfragen. Bitte eine Minute warten.")
			return
		}
		tok := bearer(r)
		if tok == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pk95monitoring"`)
			writeErr(w, http.StatusUnauthorized, "API-Token fehlt (Authorization: Bearer …).")
			return
		}
		if _, err := s.auths.CheckAPIToken(tok); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pk95monitoring", error="invalid_token"`)
			writeErr(w, http.StatusUnauthorized, "API-Token ungültig.")
			return
		}
		next(w, r)
	})
}

func (s *Server) v1Summary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.C.Summary())
}

func (s *Server) v1Devices(w http.ResponseWriter, r *http.Request) {
	list := s.C.Summary().Devices
	if list == nil {
		list = []model.DeviceSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

func (s *Server) v1Device(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.C.DeviceSummary(id)
	if err != nil {
		fail(w, err)
		return
	}
	checks := s.C.Status(id).Checks
	if checks == nil {
		checks = []model.CheckStatus{}
	}
	rules := []model.CheckRule{}
	if dev, err := s.C.Device(id); err == nil && dev.Rules != nil {
		rules = dev.Rules
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": d, "checks": checks, "rules": rules})
}

func (s *Server) v1Alerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		list []model.Alert
		err  error
	)
	if q.Get("state") == "all" {
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		list, err = s.C.St.Alerts(r.Context(), store.AlertQuery{Before: before, Limit: intParam(q.Get("limit"), 100, 1, 1000)})
	} else {
		list, err = s.C.St.ActiveAlerts()
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alertList(list)})
}
