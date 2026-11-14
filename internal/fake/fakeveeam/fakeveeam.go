// Package fakeveeam simulates the REST API of Veeam Backup & Replication 12 (token, server
// info, job states, repository states) for tests and the demo. It must not import core.
package fakeveeam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Login of the simulated server.
const (
	User     = "monitoring"
	Password = "geheim"
)

// Job is a simulated job; times are relative to the last full hour.
type Job struct {
	ID, Name, Type, Status, Result, Repository string
	LastAgo, NextIn                            time.Duration // NextIn < 0: scheduled run did not start
	Objects                                    int
}

// Repo is a simulated repository.
type Repo struct {
	ID, Name, Type, Host, Path string
	CapacityGB, FreeGB         float64
}

// Server is the fake Veeam REST API.
type Server struct {
	mu    sync.Mutex
	srv   *httptest.Server
	jobs  []Job
	repos []Repo
	token string
	now   func() time.Time
}

// New starts the server with a few jobs: VM backups that succeeded, a backup copy with
// warnings, a failed agent backup and a disabled job.
func New() *Server {
	s := &Server{token: "tok-" + strconv.FormatInt(time.Now().UnixNano(), 36), now: time.Now}
	s.jobs = []Job{
		{ID: "a1f0c3e2-0001", Name: "Täglich – VMs", Type: "Backup", Status: "inactive", Result: "Success", Repository: "Repo NAS", LastAgo: 9 * time.Hour, NextIn: 15 * time.Hour, Objects: 6},
		{ID: "a1f0c3e2-0002", Name: "SQL-Server stündlich", Type: "Backup", Status: "inactive", Result: "Success", Repository: "Repo NAS", LastAgo: 40 * time.Minute, NextIn: 20 * time.Minute, Objects: 1},
		{ID: "a1f0c3e2-0003", Name: "Kopie Offsite", Type: "BackupCopy", Status: "inactive", Result: "Warning", Repository: "Offsite S3", LastAgo: 7 * time.Hour, NextIn: 17 * time.Hour, Objects: 6},
		{ID: "a1f0c3e2-0004", Name: "Laptops (Agent)", Type: "WindowsAgentBackupWorkstationPolicy", Status: "inactive", Result: "Failed", Repository: "Repo NAS", LastAgo: 11 * time.Hour, NextIn: 13 * time.Hour, Objects: 14},
		{ID: "a1f0c3e2-0005", Name: "Archiv monatlich", Type: "Backup", Status: "disabled", Result: "Success", Repository: "Repo NAS", LastAgo: 40 * 24 * time.Hour, Objects: 6},
	}
	s.repos = []Repo{
		{ID: "b2e1-0001", Name: "Repo NAS", Type: "LinuxHardened", Host: "nas01", Path: "/mnt/backup/veeam", CapacityGB: 16384, FreeGB: 4710},
		{ID: "b2e1-0002", Name: "Offsite S3", Type: "AmazonS3", Host: "", Path: "s3://kunde-offsite", CapacityGB: 0},
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

// Fingerprint returns the SHA-256 fingerprint of the server certificate.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// Config returns a working configuration.
func (s *Server) Config() model.VeeamConfig {
	return model.VeeamConfig{Port: s.Port(), Username: User, Password: Password, Fingerprint: s.Fingerprint()}
}

// SetJobResult changes the last result of a job (by name).
func (s *Server) SetJobResult(name, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].Name == name {
			s.jobs[i].Result = result
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("x-api-version") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"errorCode": "BadRequest", "message": "x-api-version header is missing"})
		return
	}
	if r.URL.Path == "/api/oauth2/token" && r.Method == http.MethodPost {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "password" || r.PostForm.Get("username") != User || r.PostForm.Get("password") != Password {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"errorCode": "Unauthorized", "message": "Invalid credentials"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": s.token, "token_type": "bearer", "expires_in": 900})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"errorCode": "Unauthorized", "message": "Unauthorized"})
		return
	}
	hour := s.now().Truncate(time.Hour)
	ts := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000-07:00") }
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/api/v1/serverInfo":
		writeJSON(w, http.StatusOK, map[string]any{"vbrId": "6745a759-2205-4cd2-b172-8ec8f7e60ef8", "name": "veeam01",
			"buildVersion": "12.3.1.1139", "patches": []string{}, "databaseVendor": "PostgreSQL"})
	case "/api/v1/jobs/states":
		out := []map[string]any{}
		for _, j := range s.jobs {
			m := map[string]any{"id": j.ID, "name": j.Name, "type": j.Type, "description": "", "status": j.Status,
				"lastResult": j.Result, "workload": "Server", "objectsCount": j.Objects, "repositoryName": j.Repository,
				"lastRun": ts(hour.Add(-j.LastAgo))}
			if j.NextIn != 0 && j.Status != "disabled" {
				m["nextRun"] = ts(hour.Add(j.NextIn))
			}
			out = append(out, m)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "pagination": map[string]int{"total": len(out), "count": len(out), "skip": 0, "limit": 1000}})
	case "/api/v1/backupInfrastructure/repositories/states":
		out := []map[string]any{}
		for _, rp := range s.repos {
			used := rp.CapacityGB - rp.FreeGB
			if rp.CapacityGB <= 0 {
				used = 0
			}
			out = append(out, map[string]any{"id": rp.ID, "name": rp.Name, "type": rp.Type, "hostName": rp.Host, "path": rp.Path,
				"capacityGB": rp.CapacityGB, "freeGB": rp.FreeGB, "usedSpaceGB": used})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"errorCode": "NotFound", "message": "Not found"})
	}
}
