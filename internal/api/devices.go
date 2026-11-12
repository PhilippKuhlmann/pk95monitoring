package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/collect/nut"
	"pk95monitoring/internal/collect/proxmox"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ov, err := s.C.Overview(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func rows(list []core.DeviceRow) []core.DeviceRow {
	if list == nil {
		return []core.DeviceRow{}
	}
	return list
}

func strs(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows(s.C.Rows()), "groups": strs(s.C.Groups())})
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.C.Device(id)
	if err != nil {
		fail(w, err)
		return
	}
	alerts, err := s.C.St.Alerts(r.Context(), store.AlertQuery{DeviceID: id, Limit: 20})
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"device": redactDevice(d), "status": s.C.Status(id), "alerts": alertList(alerts)}
	if d.Type != model.TypeAgent {
		list := []attachedOut{}
		for _, a := range s.C.Attached(id) {
			list = append(list, attachedOut{Device: redactDevice(a), Status: s.C.Status(a.ID)})
		}
		out["attached"] = list
	} else if p, err := s.C.Device(d.ParentID); d.ParentID != "" && err == nil && p.Type != model.TypeAgent {
		out["parent"] = parentOut{ID: p.ID, Name: p.Name, Type: p.Type}
	} else {
		out["suggest"] = s.C.Suggest(id)
	}
	writeJSON(w, http.StatusOK, out)
}

// attachedOut is an agent attached to the device shown (GET /api/devices/{id}).
type attachedOut struct {
	Device deviceOut          `json:"device"`
	Status model.DeviceStatus `json:"status"`
}

// parentOut is the device an attached agent belongs to.
type parentOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// attach attaches an agent to the device {id}: {agent_id, node} → {device: parent}.
func (s *Server) attach(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Node    string `json:"node"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, errs, err := s.C.Attach(r.PathValue("id"), req.AgentID, req.Node)
	if fieldErrs(w, errs) {
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": redactDevice(p)})
}

// detach makes the attached agent {id} a standalone device again → {device}.
func (s *Server) detach(w http.ResponseWriter, r *http.Request) {
	d, err := s.C.Detach(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": redactDevice(d)})
}

// saveDevice handles POST /api/devices (new) and PUT /api/devices/{id}.
func (s *Server) saveDevice(w http.ResponseWriter, r *http.Request) {
	var d model.Device
	if !decode(w, r, &d) {
		return
	}
	d.ID = r.PathValue("id")
	if d.ID != "" {
		if _, err := s.C.Device(d.ID); err != nil {
			fail(w, err)
			return
		}
	}
	saved, errs, err := s.C.SaveDevice(d)
	if fieldErrs(w, errs) {
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, redactDevice(saved))
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.C.DeleteDevice(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) pollDevice(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	st, err := s.C.PollNow(ctx, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st})
}

func (s *Server) maintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Until int64  `json:"until"`
		Note  string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	errs := map[string]string{}
	if req.Until != 0 && req.Until <= time.Now().Unix() {
		errs["until"] = "Liegt in der Vergangenheit."
	}
	if len([]rune(req.Note)) > 500 {
		errs["note"] = "Höchstens 500 Zeichen."
	}
	if fieldErrs(w, errs) {
		return
	}
	d, err := s.C.SetMaintenance(r.PathValue("id"), req.Until, req.Note)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, redactDevice(d))
}

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.C.Device(id); err != nil {
		fail(w, err)
		return
	}
	list, err := s.C.St.SeriesList(id)
	if err != nil {
		fail(w, err)
		return
	}
	if list == nil {
		list = []store.Series{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": list})
}

// metricRanges are the chart ranges of the UI with their default bucket size (≈ 60–365 points).
var metricRanges = map[string]struct{ span, step int64 }{
	"1h":  {3600, 60},
	"6h":  {6 * 3600, 120},
	"24h": {24 * 3600, 300},
	"7d":  {7 * 86400, 1800},
	"30d": {30 * 86400, 7200},
	"1y":  {365 * 86400, 86400},
}

// metricStep is the bucket size for a range: at least the poll interval of the device
// (rounded up to whole minutes), so a device polled every 5 minutes does not show a gap
// between every two points of a short range.
func metricStep(rangeStep int64, interval int) int64 {
	iv := (int64(interval) + 59) / 60 * 60
	return max(rangeStep, iv)
}

const maxChartSeries = 16

type seriesPoints struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	Instance string        `json:"instance"`
	Unit     string        `json:"unit"`
	Points   []store.Point `json:"points"`
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.C.Device(id)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	rg, okRange := metricRanges[q.Get("range")]
	if q.Get("range") == "" {
		rg, okRange = metricRanges["24h"], true
	}
	if !okRange {
		writeErr(w, http.StatusBadRequest, "Unbekannter Zeitraum.")
		return
	}
	ids, err := parseIDs(q.Get("series"))
	if err != nil || len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "Ungültige Auswahl der Messreihen.")
		return
	}
	if len(ids) > maxChartSeries {
		writeErr(w, http.StatusBadRequest, "Höchstens "+strconv.Itoa(maxChartSeries)+" Messreihen auf einmal.")
		return
	}
	all, err := s.C.St.SeriesList(id)
	if err != nil {
		fail(w, err)
		return
	}
	byID := map[int64]store.Series{}
	for _, se := range all {
		byID[se.ID] = se
	}
	var want []int64
	for _, sid := range ids {
		if _, ok := byID[sid]; ok { // only series of this device
			want = append(want, sid)
		}
	}
	step := metricStep(rg.step, d.PollInterval())
	to := time.Now().Unix()
	from := to - rg.span
	out := []seriesPoints{}
	if len(want) > 0 {
		pts, err := s.C.St.Query(want, from, to, step)
		if err != nil {
			fail(w, err)
			return
		}
		for _, sid := range want {
			se := byID[sid]
			p := pts[sid]
			if p == nil {
				p = []store.Point{}
			}
			out = append(out, seriesPoints{ID: se.ID, Name: se.Name, Instance: se.Instance, Unit: se.Unit, Points: p})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "step": step, "series": out})
}

// parseIDs parses "1,2,3" (duplicates removed, order kept).
func parseIDs(s string) ([]int64, error) {
	var ids []int64
	seen := map[int64]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n <= 0 {
			return nil, strconv.ErrSyntax
		}
		if !seen[n] {
			seen[n] = true
			ids = append(ids, n)
		}
	}
	return ids, nil
}

// ---- connection tests ----

func needHost(host string) map[string]string {
	if strings.TrimSpace(host) == "" {
		return map[string]string{"host": "Adresse angeben."}
	}
	return nil
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if !decode(w, r, &req) || fieldErrs(w, needHost(req.Host)) {
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		fieldErrs(w, map[string]string{"proxmox.port": "Port zwischen 1 und 65535."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	fp, err := proxmox.Probe(ctx, strings.TrimSpace(req.Host), req.Port)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp})
}

func (s *Server) testProxmox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string              `json:"id"`
		Host    string              `json:"host"`
		Proxmox model.ProxmoxConfig `json:"proxmox"`
	}
	if !decode(w, r, &req) || fieldErrs(w, needHost(req.Host)) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.C.TestProxmox(ctx, req.ID, strings.TrimSpace(req.Host), req.Proxmox)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	nodes := res.Nodes
	if nodes == nil {
		nodes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": res.Version, "nodes": nodes, "guests": res.Guests})
}

func (s *Server) testSNMP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string           `json:"id"`
		Host string           `json:"host"`
		SNMP model.SNMPConfig `json:"snmp"`
	}
	if !decode(w, r, &req) || fieldErrs(w, needHost(req.Host)) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := s.C.TestSNMP(ctx, req.ID, strings.TrimSpace(req.Host), req.SNMP)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	out := map[string]any{"ok": true, "sys_name": res.SysName, "sys_descr": res.SysDescr, "profile": res.Profile, "interfaces": res.Interfaces}
	if res.Interfaces == nil {
		out["interfaces"] = []any{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) testNUT(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string          `json:"id"`
		Host string          `json:"host"`
		NUT  model.NUTConfig `json:"nut"`
	}
	if !decode(w, r, &req) || fieldErrs(w, needHost(req.Host)) {
		return
	}
	if req.NUT.Port < 0 || req.NUT.Port > 65535 {
		fieldErrs(w, map[string]string{"nut.port": "Port zwischen 1 und 65535."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.C.TestNUT(ctx, req.ID, strings.TrimSpace(req.Host), req.NUT)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	ups := res.UPS
	if ups == nil {
		ups = []nut.UPSInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": res.Server, "ups": ups, "message": ""})
}

var netTypes = map[string]bool{model.NetPing: true, model.NetHTTP: true, model.NetTCP: true, model.NetDNS: true, model.NetTLS: true}

func (s *Server) testNetCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host  string         `json:"host"`
		Check model.NetCheck `json:"check"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !netTypes[req.Check.Type] {
		fieldErrs(w, map[string]string{"type": "Art des Checks wählen."})
		return
	}
	if req.Check.Target == "" && (req.Check.Type != model.NetPing || strings.TrimSpace(req.Host) == "") {
		fieldErrs(w, map[string]string{"target": "Ziel angeben."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.C.TestNetCheck(ctx, strings.TrimSpace(req.Host), req.Check))
}

// ---- alerts and events ----

func alertList(list []model.Alert) []model.Alert {
	if list == nil {
		return []model.Alert{}
	}
	return list
}

// intParam parses a query value, def when empty or invalid, clamped to [lo, hi].
func intParam(v string, def, lo, hi int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := intParam(q.Get("limit"), 50, 1, 500)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	list, err := s.C.St.Alerts(r.Context(), store.AlertQuery{DeviceID: q.Get("device"), ActiveOnly: q.Get("state") == "active",
		Before: before, Limit: limit + 1})
	if err != nil {
		fail(w, err)
		return
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alertList(list), "more": more})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Nicht gefunden.")
		return
	}
	if err := s.C.AckAlert(id, currentUser(r).Username); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := intParam(q.Get("limit"), 50, 1, 500)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	evs, err := s.C.St.Events(r.Context(), q.Get("severity"), before, limit+1)
	if err != nil {
		fail(w, err)
		return
	}
	more := len(evs) > limit
	if more {
		evs = evs[:limit]
	}
	if evs == nil {
		evs = []store.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs, "more": more})
}
