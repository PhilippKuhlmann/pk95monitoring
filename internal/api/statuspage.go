package api

import (
	"errors"
	"io/fs"
	"net/http"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/store"
)

// Status page: configured in the UI (session), shown publicly behind its secret link
// /status/<token> (the page) and /api/status/<token> (its data, rate-limited like the
// external API).

// statusPageOut is the configuration with a preview of the public view.
type statusPageOut struct {
	Page    core.StatusPage   `json:"page"`
	Path    string            `json:"path"` // "/status/<token>", "" before the first save
	URL     string            `json:"url"`  // full link with the public URL; "" without one (the UI uses its origin)
	Preview core.PublicStatus `json:"preview"`
}

func (s *Server) statusPageOut(w http.ResponseWriter, r *http.Request, p core.StatusPage) {
	prev, err := s.C.StatusPreview(r.Context())
	if err != nil {
		internalErr(w, "status page preview", err)
		return
	}
	out := statusPageOut{Page: p, Preview: prev}
	if p.Token != "" {
		out.Path = "/status/" + p.Token
		if u := s.C.PublicURL(); u != "" {
			out.URL = u + out.Path
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getStatusPage(w http.ResponseWriter, r *http.Request) {
	s.statusPageOut(w, r, s.C.StatusPage())
}

func (s *Server) putStatusPage(w http.ResponseWriter, r *http.Request) {
	var p core.StatusPage
	if !decode(w, r, &p) {
		return
	}
	for _, cp := range p.Components {
		if len(cp.Devices) > 10000 || len(cp.Folders) > 1000 {
			writeErr(w, http.StatusBadRequest, "Zu viele Geräte oder Ordner.")
			return
		}
	}
	saved, errs, err := s.C.SaveStatusPage(p)
	if fieldErrs(w, errs) {
		return
	}
	if err != nil {
		internalErr(w, "save status page", err)
		return
	}
	s.statusPageOut(w, r, saved)
}

func (s *Server) rotateStatusToken(w http.ResponseWriter, r *http.Request) {
	p, err := s.C.RotateStatusToken()
	if err != nil {
		internalErr(w, "rotate status token", err)
		return
	}
	s.statusPageOut(w, r, p)
}

// publicStatus answers the data of the status page.
func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !s.allow(s.clientIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "Zu viele Anfragen. Bitte eine Minute warten.")
		return
	}
	st, err := s.C.PublicStatus(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "Diese Statusseite gibt es nicht.")
		return
	}
	if err != nil {
		internalErr(w, "public status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// statusHTML serves the status page; it loads its data from /api/status/<token>. An unknown
// or switched-off link gets the same page with 404 (it then says so).
func (s *Server) statusHTML(w http.ResponseWriter, r *http.Request) {
	page, err := fs.ReadFile(s.Web, "status.html")
	if err != nil {
		internalErr(w, "status page", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	code := http.StatusOK
	if !s.C.StatusTokenValid(r.PathValue("token")) {
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}
