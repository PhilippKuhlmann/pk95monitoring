package api

import (
	"errors"
	"net/http"

	"pk95monitoring/internal/core"
)

// Network scan (page "Netzwerk-Scan"): GET the state, POST to start, POST …/ignore to hide
// an address from the suggestions.

func (s *Server) getScan(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.C.ScanState())
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ranges    []string `json:"ranges"`
		Community string   `json:"community"`
	}
	if !decode(w, r, &req) {
		return
	}
	errs, err := s.C.StartScan(req.Ranges, req.Community)
	if fieldErrs(w, errs) {
		return
	}
	if errors.Is(err, core.ErrScanRunning) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		internalErr(w, "start scan", err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.C.ScanState())
}

func (s *Server) ignoreScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP      string `json:"ip"`
		Ignored bool   `json:"ignored"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.C.SetScanIgnore(req.IP, req.Ignored); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.C.ScanState())
}
