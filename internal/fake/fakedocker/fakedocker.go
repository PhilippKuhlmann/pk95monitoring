// Package fakedocker simulates the Docker Engine API (the endpoints the agent reads: /version,
// /info, /containers/json, /containers/{id}/json and /containers/{id}/stats) on a test HTTP
// server. Containers are set with Set; every stats call advances the network counters.
package fakedocker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Container is one simulated container.
type Container struct {
	ID, Name, Image string
	State           string // running, exited, restarting, paused, created, dead
	Health          string // healthy, unhealthy, starting or ""
	HealthOut       string
	Failing         int
	ExitCode        int
	OOM             bool
	Policy          string // no, always, unless-stopped, on-failure
	Restarts        int
	Project         string
	Started         time.Time
	Finished        time.Time
	PublicPort      int
	PrivatePort     int
	CPUPct          float64 // share of all host CPUs
	MemUsed         float64 // bytes without cache
	MemLimit        float64 // 0 = no limit (the API reports the host's RAM)
	NetRate         float64 // bytes per stats call, received (half of it sent)
}

// Server is a running fake engine. All methods are safe for concurrent use.
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	podman bool
	cts    []*Container
	net    map[string]float64
	calls  map[string]int
}

// MemTotal is the RAM of the simulated host.
const MemTotal = 8 << 30

// New starts a fake engine.
func New() *Server {
	s := &Server{net: map[string]float64{}, calls: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// SetPodman makes the engine answer as Podman.
func (s *Server) SetPodman(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.podman = on
}

// Set replaces the containers.
func (s *Server) Set(cts ...Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cts = nil
	for i := range cts {
		c := cts[i]
		s.cts = append(s.cts, &c)
	}
}

// Update changes the container with this name.
func (s *Server) Update(name string, fn func(*Container)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cts {
		if c.Name == name {
			fn(c)
		}
	}
}

// Calls returns how often paths with this prefix were requested.
func (s *Server) Calls(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for p, c := range s.calls {
		if strings.HasPrefix(p, prefix) {
			n += c
		}
	}
	return n
}

func ts(t time.Time) string {
	if t.IsZero() {
		return "0001-01-01T00:00:00Z"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[r.URL.Path]++
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/version":
		name := "Docker Engine - Community"
		if s.podman {
			name = "Podman Engine"
		}
		write(map[string]any{"Version": "27.3.1", "ApiVersion": "1.47", "Platform": map[string]string{"Name": name}})
	case p == "/info":
		write(map[string]any{"Driver": "overlay2", "DockerRootDir": "/var/lib/docker", "NCPU": 4, "MemTotal": MemTotal, "Images": 9})
	case p == "/containers/json":
		out := []map[string]any{}
		for _, c := range s.cts {
			labels := map[string]string{}
			if c.Project != "" {
				labels["com.docker.compose.project"] = c.Project
				labels["com.docker.compose.service"] = strings.TrimSuffix(strings.TrimPrefix(c.Name, c.Project+"-"), "-1")
			}
			ports := []map[string]any{}
			if c.PublicPort > 0 {
				ports = append(ports, map[string]any{"IP": "0.0.0.0", "PrivatePort": c.PrivatePort, "PublicPort": c.PublicPort, "Type": "tcp"},
					map[string]any{"IP": "::", "PrivatePort": c.PrivatePort, "PublicPort": c.PublicPort, "Type": "tcp"})
			}
			out = append(out, map[string]any{"Id": c.ID, "Names": []string{"/" + c.Name}, "Image": c.Image, "State": c.State,
				"Labels": labels, "Ports": ports, "Created": c.Started.Add(-time.Hour).Unix()})
		}
		write(out)
	case strings.HasPrefix(p, "/containers/"):
		id, what, _ := strings.Cut(strings.TrimPrefix(p, "/containers/"), "/")
		var c *Container
		for _, x := range s.cts {
			if x.ID == id {
				c = x
			}
		}
		if c == nil {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]string{"message": "No such container: " + id})
			return
		}
		switch what {
		case "json":
			st := map[string]any{"Status": c.State, "OOMKilled": c.OOM, "ExitCode": c.ExitCode, "Error": "",
				"StartedAt": ts(c.Started), "FinishedAt": ts(c.Finished)}
			if c.Health != "" {
				st["Health"] = map[string]any{"Status": c.Health, "FailingStreak": c.Failing,
					"Log": []map[string]any{{"ExitCode": 0, "Output": "ok"}, {"ExitCode": 1, "Output": c.HealthOut + "\n"}}}
			}
			write(map[string]any{"RestartCount": c.Restarts, "State": st,
				"HostConfig": map[string]any{"NanoCpus": 0, "RestartPolicy": map[string]string{"Name": c.Policy}}})
		case "stats":
			if c.State != "running" {
				write(map[string]any{"cpu_stats": map[string]any{}, "precpu_stats": map[string]any{}, "memory_stats": map[string]any{}})
				return
			}
			s.net[c.ID] += c.NetRate
			sys0, sys1 := 1e12, 1e12+4e9
			cpu0 := 5e10
			cpu1 := cpu0 + (sys1-sys0)*c.CPUPct/100
			limit := c.MemLimit
			if limit == 0 {
				limit = MemTotal
			}
			write(map[string]any{
				"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": cpu1}, "system_cpu_usage": sys1, "online_cpus": 4},
				"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": cpu0}, "system_cpu_usage": sys0, "online_cpus": 4},
				"memory_stats": map[string]any{"usage": c.MemUsed + 50<<20, "limit": limit, "stats": map[string]any{"inactive_file": 50 << 20}},
				"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": s.net[c.ID], "tx_bytes": s.net[c.ID] / 2}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]string{"message": "page not found"})
	}
}
