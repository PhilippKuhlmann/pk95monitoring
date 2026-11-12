// Package fakepve simulates the Proxmox VE REST API (api2/json) for tests and the demo mode:
// a cluster "demo" with three nodes, guests, storages, ZFS pools, SMART disks, apt updates
// and vzdump tasks. Values move with time (sine + noise) so charts look alive.
// It must not import internal/core.
package fakepve

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Server is a simulated PVE API on a TLS test server.
type Server struct {
	URL     string // https://127.0.0.1:port
	TokenID string
	Secret  string

	srv          *httptest.Server
	mu           sync.Mutex
	offset       time.Duration
	nodes        []*node
	guests       []*guest
	storages     []*storage
	pools        []*pool
	disks        []*disk
	apt          map[string][]aptPkg
	backupFailed bool
	forbidden    map[string]bool
	repl         []*replJob
	ha           map[string]string // sid -> state
	ceph         *cephState        // nil: Ceph not installed
	certs        map[string][]cert // node -> certificates
	iowait       map[string]float64
}

// New starts a simulated PVE cluster. Close it when done.
func New() *Server {
	s := &Server{TokenID: "pk95mon@pve!monitoring", Secret: "5f0c1b2e-7d3a-4c9b-9e21-0a6f4b8d2c11", backupFailed: true, forbidden: map[string]bool{}}
	s.seed(time.Now())
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	s.URL = s.srv.URL
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Host returns the IP of the server.
func (s *Server) Host() string {
	u, _ := url.Parse(s.URL)
	return u.Hostname()
}

// Port returns the TCP port of the server.
func (s *Server) Port() int {
	u, _ := url.Parse(s.URL)
	p, _ := strconv.Atoi(u.Port())
	return p
}

// Fingerprint returns the SHA-256 fingerprint of the server certificate as AA:BB:….
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// Config returns a Proxmox configuration that connects to this server.
func (s *Server) Config() model.ProxmoxConfig {
	return model.ProxmoxConfig{Port: s.Port(), TokenID: s.TokenID, Secret: s.Secret, Fingerprint: s.Fingerprint(), WatchGuests: "onboot"}
}

// Now returns the fake clock (real time plus everything passed to Advance).
func (s *Server) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

func (s *Server) now() time.Time { return time.Now().Add(s.offset) }

// Advance moves the fake clock forward (counters and uptimes grow accordingly).
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset += d
}

// SetNodeOnline switches a node on or off. A node coming back has just booted: its
// running guests restart (counters reset).
func (s *Server) SetNodeOnline(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, n := range s.nodes {
		if n.name != name || n.online == on {
			continue
		}
		n.online = on
		if on {
			n.bootAt = now
			for _, g := range s.guests {
				if g.node == name && g.running() {
					g.startedAt = now
				}
			}
		}
	}
}

// SetGuestStatus sets the status ("running", "stopped", "paused") of a guest.
func (s *Server) SetGuestStatus(vmid int, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.guests {
		if g.vmid != vmid || g.status == status {
			continue
		}
		g.status = status
		if status == "running" {
			g.startedAt = s.now()
			if g.cpuBase == 0 {
				g.cpuBase, g.memBase, g.net, g.io = 0.05, 0.4, 50e3, 80e3
			}
		}
	}
}

// AddGuest adds a running guest ("qemu" or "lxc") on a node; an existing vmid is replaced.
func (s *Server) AddGuest(vmid int, name, nodeName, typ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeGuest(vmid)
	now := s.now()
	s.guests = append(s.guests, &guest{vmid: vmid, name: name, node: nodeName, typ: typ, onboot: true, status: "running", cores: 2,
		maxmem: 4 * gib, maxdisk: 64 * gib, cpuBase: 0.08, memBase: 0.45, net: 120e3, io: 200e3, startedAt: now, phase: float64(vmid%17) * 0.37})
}

// RemoveGuest deletes a guest.
func (s *Server) RemoveGuest(vmid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeGuest(vmid)
}

func (s *Server) removeGuest(vmid int) {
	s.guests = slices.DeleteFunc(s.guests, func(g *guest) bool { return g.vmid == vmid })
}

// SetBackupFailed makes the nightly backup job of pve2 fail (default) or succeed.
func (s *Server) SetBackupFailed(failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupFailed = failed
}

// SetStorageUsage fixes the usage (percent) of a storage; for shared storages on all nodes.
func (s *Server) SetStorageUsage(nodeName, name string, pct float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.storages {
		if st.name == name && (st.node == nodeName || st.shared) {
			st.pct, st.fixed = pct/100, true
		}
	}
}

// SetZFSHealth sets the health (ONLINE, DEGRADED, FAULTED …) of a ZFS pool.
func (s *Server) SetZFSHealth(nodeName, name, health string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		if p.node == nodeName && p.name == name {
			p.health = health
		}
	}
}

// SetForbidden answers requests whose path contains pattern (e.g. "/apt/update") with 403.
func (s *Server) SetForbidden(pattern string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.forbidden[pattern] = true
	} else {
		delete(s.forbidden, pattern)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"data": nil, "message": msg})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "PVEAPIToken="+s.TokenID+"="+s.Secret {
		fail(w, http.StatusUnauthorized, "invalid token value!")
		return
	}
	if r.Method != http.MethodGet {
		fail(w, http.StatusNotImplemented, "Method '"+r.Method+" "+r.URL.Path+"' not implemented")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	for p := range s.forbidden {
		if strings.Contains(path, p) {
			fail(w, http.StatusForbidden, "Permission check failed ("+path+")")
			return
		}
	}
	data, status, msg := s.route(strings.Split(strings.Trim(path, "/"), "/"), r.URL.Query())
	if status != http.StatusOK {
		fail(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// route answers a GET; s.mu is held.
func (s *Server) route(p []string, q url.Values) (any, int, string) {
	now := s.now()
	switch {
	case len(p) == 1 && p[0] == "version":
		return map[string]any{"version": "8.3.2", "release": "8.3", "repoid": "3e76eec21c4a14a7"}, 200, ""
	case len(p) == 1 && p[0] == "nodes":
		return s.resources(now, "node"), 200, ""
	case len(p) == 2 && p[0] == "cluster" && p[1] == "status":
		return s.clusterStatus(), 200, ""
	case len(p) == 2 && p[0] == "cluster" && p[1] == "resources":
		return s.resources(now, q.Get("type")), 200, ""
	case len(p) == 4 && p[0] == "cluster" && p[1] == "ha" && p[2] == "status" && p[3] == "current":
		return s.haStatus(), 200, ""
	case len(p) == 3 && p[0] == "cluster" && p[1] == "ceph" && p[2] == "status":
		if s.ceph == nil {
			return nil, 500, "binary not installed: /usr/bin/ceph-mon"
		}
		return s.cephStatus(), 200, ""
	case len(p) == 2 && p[0] == "cluster" && p[1] == "tasks":
		tasks := s.tasks(now)
		out := make([]map[string]any, len(tasks))
		for i, t := range tasks {
			out[i] = t.json()
		}
		return out, 200, ""
	case len(p) >= 3 && p[0] == "nodes":
		n := s.node(p[1])
		if n == nil {
			return nil, 500, "hostname lookup '" + p[1] + "' failed - failed to get address info"
		}
		if !n.online {
			return nil, 595, "no route to host"
		}
		return s.nodeRoute(n, p[2:], q, now)
	}
	return nil, 501, "Method 'GET /" + strings.Join(p, "/") + "' not implemented"
}

func (s *Server) nodeRoute(n *node, p []string, q url.Values, now time.Time) (any, int, string) {
	switch strings.Join(p, "/") {
	case "tasks": // the node's task archive: finished tasks, newest first
		since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			limit = 50
		}
		out := []map[string]any{}
		for _, t := range s.tasks(now) {
			if t.node != n.name || t.start.Unix() < since || (q.Get("typefilter") != "" && t.typ != q.Get("typefilter")) {
				continue
			}
			if len(out) == limit {
				break
			}
			out = append(out, t.json())
		}
		return out, 200, ""
	case "status":
		return s.nodeStatus(n, now), 200, ""
	case "disks/zfs":
		var out []map[string]any
		for _, pl := range s.pools {
			if pl.node == n.name {
				alloc := pl.size * pl.allocPct
				out = append(out, map[string]any{"name": pl.name, "health": pl.health, "size": int64(pl.size),
					"alloc": int64(alloc), "free": int64(pl.size - alloc), "frag": pl.frag, "dedup": 1.0})
			}
		}
		return out, 200, ""
	case "disks/list":
		var out []map[string]any
		for _, d := range s.disks {
			if d.node == n.name {
				var wear any = "N/A"
				if d.wearout >= 0 {
					wear = d.wearout
				}
				out = append(out, map[string]any{"devpath": "/dev/" + d.dev, "model": d.model, "serial": d.serial,
					"size": int64(d.size), "type": d.typ, "health": d.health, "wearout": wear, "gpt": 1, "used": "ZFS"})
			}
		}
		return out, 200, ""
	case "apt/update":
		return s.apt[n.name], 200, ""
	case "disks/smart":
		return s.smart(n, q.Get("disk"))
	case "replication":
		return s.replication(n, now), 200, ""
	case "certificates/info":
		return s.certInfo(n), 200, ""
	}
	if len(p) == 3 && p[0] == "disks" && p[1] == "zfs" {
		return s.zfsDetail(n, p[2], now)
	}
	// qemu/<id>/config, lxc/<id>/config, tasks/<upid>/log
	if len(p) == 3 && (p[0] == "qemu" || p[0] == "lxc") && p[2] == "config" {
		for _, g := range s.guests {
			if strconv.Itoa(g.vmid) == p[1] && g.typ == p[0] && g.node == n.name {
				return g.config(), 200, ""
			}
		}
		return nil, 500, "Configuration file 'nodes/" + n.name + "/" + p[0] + "/" + p[1] + ".conf' does not exist"
	}
	if len(p) == 3 && p[0] == "tasks" && p[2] == "log" {
		for _, t := range s.tasks(now) {
			if t.upid == p[1] {
				out := make([]map[string]any, len(t.log))
				for i, l := range t.log {
					out[i] = map[string]any{"n": i + 1, "t": l}
				}
				return out, 200, ""
			}
		}
		return nil, 500, "no such task"
	}
	return nil, 501, "Method 'GET /nodes/" + n.name + "/" + strings.Join(p, "/") + "' not implemented"
}

func (s *Server) node(name string) *node {
	for _, n := range s.nodes {
		if n.name == name {
			return n
		}
	}
	return nil
}

func (s *Server) clusterStatus() []map[string]any {
	online := 0
	for _, n := range s.nodes {
		if n.online {
			online++
		}
	}
	quorate := 0
	if online*2 > len(s.nodes) {
		quorate = 1
	}
	out := []map[string]any{{"type": "cluster", "id": "cluster", "name": "demo", "nodes": len(s.nodes), "quorate": quorate, "version": 5}}
	for i, n := range s.nodes {
		out = append(out, map[string]any{"type": "node", "id": "node/" + n.name, "name": n.name, "nodeid": i + 1,
			"ip": n.ip, "online": b2i(n.online), "local": b2i(i == 0), "level": ""})
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) nodeStatus(n *node, now time.Time) map[string]any {
	cpu, mem := n.cpuAt(now), n.memAt(now)
	load := cpu * float64(n.cpus) * 0.9
	rootUsed := n.root * (n.rootPct + 0.002*wave(now, 24*time.Hour, n.phase))
	swapTotal := 8.0 * gib
	swapUsed := swapTotal * clamp(0.03+0.02*wave(now, 12*time.Hour, n.phase), 0, 1)
	return map[string]any{
		"cpu":        cpu,
		"wait":       s.ioWaitAt(n, now),
		"loadavg":    []string{fmt.Sprintf("%.2f", load), fmt.Sprintf("%.2f", load*0.95), fmt.Sprintf("%.2f", load*0.9)},
		"memory":     map[string]any{"total": int64(n.mem), "used": int64(n.mem * mem), "free": int64(n.mem * (1 - mem))},
		"swap":       map[string]any{"total": int64(swapTotal), "used": int64(swapUsed), "free": int64(swapTotal - swapUsed)},
		"rootfs":     map[string]any{"total": int64(n.root), "used": int64(rootUsed), "avail": int64(n.root - rootUsed), "free": int64(n.root - rootUsed)},
		"uptime":     int64(now.Sub(n.bootAt).Seconds()),
		"cpuinfo":    map[string]any{"model": n.cpuModel, "cpus": n.cpus, "sockets": n.sockets, "cores": n.cpus / 2 / n.sockets, "mhz": "3800.000", "hvm": "1"},
		"pveversion": "pve-manager/8.3.2/3e76eec21c4a14a7",
		"kversion":   "Linux 6.8.12-5-pve #1 SMP PREEMPT_DYNAMIC PMX 6.8.12-5 (2024-12-03T10:26Z)",
		"boot-info":  map[string]any{"mode": "efi", "secureboot": 0},
	}
}

// resources is /cluster/resources (typ "", "node", "vm" or "storage").
func (s *Server) resources(now time.Time, typ string) []map[string]any {
	out := []map[string]any{}
	online := map[string]bool{}
	for _, n := range s.nodes {
		online[n.name] = n.online
		if typ != "" && typ != "node" {
			continue
		}
		r := map[string]any{"id": "node/" + n.name, "type": "node", "node": n.name, "status": "offline", "level": ""}
		if n.online {
			mem := n.memAt(now)
			r["status"] = "online"
			r["cpu"], r["maxcpu"] = n.cpuAt(now), n.cpus
			r["mem"], r["maxmem"] = int64(n.mem*mem), int64(n.mem)
			r["disk"], r["maxdisk"] = int64(n.root*n.rootPct), int64(n.root)
			r["uptime"] = int64(now.Sub(n.bootAt).Seconds())
		}
		out = append(out, r)
	}
	for _, g := range s.guests {
		if typ != "" && typ != "vm" {
			continue
		}
		r := map[string]any{"id": g.typ + "/" + strconv.Itoa(g.vmid), "type": g.typ, "vmid": g.vmid, "name": g.name,
			"node": g.node, "status": g.status, "template": b2i(g.template), "maxcpu": g.cores,
			"maxmem": int64(g.maxmem), "maxdisk": int64(g.maxdisk), "disk": 0, "cpu": 0, "mem": 0, "uptime": 0,
			"netin": 0, "netout": 0, "diskread": 0, "diskwrite": 0}
		if !online[g.node] {
			r["status"] = "unknown"
		} else if g.running() {
			r["cpu"], r["mem"] = g.cpuAt(now), int64(g.memAt(now))
			r["uptime"] = int64(now.Sub(g.startedAt).Seconds())
			r["netin"] = int64(counter(g.net, g.startedAt, now, 47*time.Minute, g.phase))
			r["netout"] = int64(counter(g.net*0.6, g.startedAt, now, 31*time.Minute, g.phase+1))
			r["diskread"] = int64(counter(g.io*0.7, g.startedAt, now, 53*time.Minute, g.phase+2))
			r["diskwrite"] = int64(counter(g.io*0.3, g.startedAt, now, 29*time.Minute, g.phase+3))
			if g.typ == "lxc" {
				r["disk"] = int64(g.maxdisk * 0.35)
			}
		}
		out = append(out, r)
	}
	for _, st := range s.storages {
		if typ != "" && typ != "storage" {
			continue
		}
		r := map[string]any{"id": "storage/" + st.node + "/" + st.name, "type": "storage", "storage": st.name,
			"node": st.node, "plugintype": st.typ, "content": st.content, "shared": b2i(st.shared), "status": "unknown"}
		if online[st.node] {
			r["status"], r["disk"], r["maxdisk"] = "available", int64(st.usedAt(now)), int64(st.total)
		}
		out = append(out, r)
	}
	return out
}

func (g *guest) config() map[string]any {
	cfg := map[string]any{"name": g.name, "cores": g.cores, "memory": int64(g.maxmem) >> 20, "digest": "3f2a9c"}
	if g.typ == "lxc" {
		cfg["hostname"] = g.name
		delete(cfg, "name")
	}
	if g.onboot {
		cfg["onboot"] = 1
	}
	if g.template {
		cfg["template"] = 1
	}
	return cfg
}
