// Package fakepbs simulates the REST API of Proxmox Backup Server 3 (version, datastore usage,
// namespaces, snapshots, tasks) for tests and the demo. It must not import core.
package fakepbs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Token of the simulated server.
const (
	TokenID = "monitoring@pbs!pk95"
	Secret  = "c0ffee00-1234-4abc-9def-0123456789ab"
)

// Store is a simulated datastore; FullInDays 0 = no estimate.
type Store struct {
	Name              string
	TotalGiB, UsedGiB float64
	FullInDays        int
	Namespaces        []string // besides the root namespace
	Error             string
}

// Snapshot is a simulated snapshot; times are relative to the last full hour.
type Snapshot struct {
	Store, NS, Type, ID, Comment string
	Ago                          time.Duration
	SizeGiB                      float64
	Verify                       string // "", "ok", "failed"
}

// Task is a simulated task.
type Task struct {
	Type, ID, Status string
	Ago              time.Duration
	Running          bool
}

// Server is the fake PBS API.
type Server struct {
	mu     sync.Mutex
	srv    *httptest.Server
	stores []Store
	snaps  []Snapshot
	tasks  []Task
	now    func() time.Time
}

// New starts the server with two datastores, VM, container and host backups (one group whose
// newest snapshot failed verification), and jobs (a failed sync job, a prune job with warnings).
func New() *Server {
	s := &Server{now: time.Now}
	s.stores = []Store{
		{Name: "backup", TotalGiB: 7450, UsedGiB: 5120, FullInDays: 120, Namespaces: []string{"kunde-a"}},
		{Name: "offsite", TotalGiB: 3725, UsedGiB: 3390, FullInDays: 18},
	}
	day := 24 * time.Hour
	for _, g := range []struct {
		store, ns, typ, id, comment string
		ago                         time.Duration
		size                        float64
		verify                      string
		count                       int
	}{
		{"backup", "", "vm", "100", "dc01", 3 * time.Hour, 64, "ok", 14},
		{"backup", "", "vm", "101", "fileserver", 3 * time.Hour, 512, "failed", 14},
		{"backup", "", "ct", "200", "proxy", 3 * time.Hour, 4, "ok", 14},
		{"backup", "", "host", "nas01", "", 26 * time.Hour, 220, "", 7},
		{"backup", "kunde-a", "vm", "100", "app01", 5 * time.Hour, 48, "ok", 7},
		{"offsite", "", "vm", "100", "dc01", 20 * time.Hour, 64, "ok", 4},
		{"offsite", "", "vm", "101", "fileserver", 20 * time.Hour, 512, "", 4},
		{"offsite", "", "host", "laptop-gf", "", 9 * day, 80, "ok", 3},
	} {
		for i := range g.count {
			v := "ok"
			if i == 0 {
				v = g.verify
			}
			s.snaps = append(s.snaps, Snapshot{Store: g.store, NS: g.ns, Type: g.typ, ID: g.id, Comment: g.comment,
				Ago: g.ago + time.Duration(i)*day, SizeGiB: g.size, Verify: v})
		}
	}
	s.tasks = []Task{
		{Type: "verificationjob", ID: "backup:v-daily", Status: "OK", Ago: 2 * time.Hour},
		{Type: "verificationjob", ID: "backup:v-daily", Status: "OK", Ago: 26 * time.Hour},
		{Type: "syncjob", ID: "backup:offsite:s-nightly", Status: "connection error: Connection timed out (os error 110)", Ago: 4 * time.Hour},
		{Type: "syncjob", ID: "backup:offsite:s-nightly", Status: "OK", Ago: 28 * time.Hour},
		{Type: "prunejob", ID: "backup:p-daily", Status: "WARNINGS: 1", Ago: 5 * time.Hour},
		{Type: "garbage_collection", ID: "backup", Status: "OK", Ago: 30 * time.Hour},
		{Type: "garbage_collection", ID: "offsite", Status: "OK", Ago: 50 * time.Hour},
		{Type: "garbage_collection", ID: "offsite", Running: true},
		{Type: "backup", ID: "backup:vm/100", Status: "OK", Ago: 3 * time.Hour},
	}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Host returns the address of the server.
func (s *Server) Host() string {
	h, _, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	return h
}

// Port returns the port of the server.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// Fingerprint returns the SHA-256 fingerprint of the server certificate with colons, as the
// PBS dashboard shows it.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	h := hex.EncodeToString(sum[:])
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Config returns a working configuration.
func (s *Server) Config() model.PBSConfig {
	return model.PBSConfig{Port: s.Port(), TokenID: TokenID, Secret: Secret, Fingerprint: s.Fingerprint()}
}

// SetTaskStatus sets the status of the newest finished task of a job (type and ID).
func (s *Server) SetTaskStatus(typ, id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].Type == typ && s.tasks[i].ID == id && !s.tasks[i].Running {
			s.tasks[i].Status = status
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func data(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, map[string]any{"data": v}) }

const gib = 1 << 30

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "PBSAPIToken="+TokenID+":"+Secret {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"data": nil, "message": "authentication failed - invalid token"})
		return
	}
	hour := s.now().Truncate(time.Hour)
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api2/json"), "/")
	switch {
	case path == "/version":
		data(w, map[string]string{"version": "3.4.2", "release": "3.4", "repoid": "1a2b3c4d"})
	case path == "/status/datastore-usage":
		out := []map[string]any{}
		for _, st := range s.stores {
			m := map[string]any{"store": st.Name, "total": int64(st.TotalGiB * gib), "used": int64(st.UsedGiB * gib),
				"avail": int64((st.TotalGiB - st.UsedGiB) * gib), "gc-status": map[string]any{}}
			if st.FullInDays > 0 {
				m["estimated-full-date"] = hour.Add(time.Duration(st.FullInDays) * 24 * time.Hour).Unix()
			}
			if st.Error != "" {
				m["error"] = st.Error
			}
			out = append(out, m)
		}
		data(w, out)
	case path == "/nodes/localhost/tasks":
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		out := []map[string]any{}
		for i, t := range s.tasks {
			start := hour.Add(-t.Ago - 10*time.Minute)
			if t.Running {
				start = hour.Add(-5 * time.Minute)
			}
			if start.Unix() < since {
				continue
			}
			upid := fmt.Sprintf("UPID:pbs01:%08X:%08X:%08X:%08X:%s:%s:root@pam:", 1000+i, 2000+i, i, start.Unix(), t.Type, t.ID)
			m := map[string]any{"upid": upid, "node": "pbs01", "pid": 1000 + i, "pstart": 2000 + i, "starttime": start.Unix(),
				"worker_type": t.Type, "worker_id": t.ID, "user": "root@pam"}
			if !t.Running {
				m["endtime"] = hour.Add(-t.Ago).Unix()
				m["status"] = t.Status
			}
			out = append(out, m)
		}
		data(w, out)
	case strings.HasPrefix(path, "/admin/datastore/"):
		name, sub, _ := strings.Cut(strings.TrimPrefix(path, "/admin/datastore/"), "/")
		var st *Store
		for i := range s.stores {
			if s.stores[i].Name == name {
				st = &s.stores[i]
			}
		}
		if st == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"data": nil, "message": "no such datastore '" + name + "'"})
			return
		}
		switch sub {
		case "namespace":
			out := []map[string]any{{"ns": ""}}
			for _, ns := range st.Namespaces {
				out = append(out, map[string]any{"ns": ns})
			}
			data(w, out)
		case "snapshots":
			ns := r.URL.Query().Get("ns")
			out := []map[string]any{}
			for _, sn := range s.snaps {
				if sn.Store != name || sn.NS != ns {
					continue
				}
				m := map[string]any{"backup-type": sn.Type, "backup-id": sn.ID, "backup-time": hour.Add(-sn.Ago).Unix(),
					"size": int64(sn.SizeGiB * gib), "owner": "root@pam", "protected": false,
					"files": []map[string]any{{"filename": "index.json.blob", "size": 512}}}
				if sn.Comment != "" {
					m["comment"] = sn.Comment
				}
				if sn.Verify != "" {
					m["verification"] = map[string]string{"state": sn.Verify, "upid": "UPID:pbs01:verify"}
				}
				out = append(out, m)
			}
			data(w, out)
		default:
			writeJSON(w, http.StatusNotImplemented, map[string]any{"data": nil, "message": "Method not implemented"})
		}
	default:
		writeJSON(w, http.StatusNotImplemented, map[string]any{"data": nil, "message": "Method not implemented"})
	}
}
