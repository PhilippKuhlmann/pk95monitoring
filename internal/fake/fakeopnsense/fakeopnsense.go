// Package fakeopnsense simulates the REST API of an OPNsense firewall (the read endpoints the
// collector uses) on an httptest TLS server: OPNsense 25.7 with two WAN gateways, an IPsec
// tunnel, WireGuard peers, CARP and services. Basic auth with Key and Secret.
package fakeopnsense

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Credentials the server accepts.
const (
	Key    = "Wq2n6Yp0p3Kc9Vd7Hk1Lm4Rt8Sx5Ub0Zf2Ga6Je3"
	Secret = "Fs9Lp2Qw7Ne4Rt1Yu8Io5Pa3Sd6Fg0Hj2Kl7Zx4Cv9Bn1Mq"
)

// Server is a running fake firewall. All methods are safe for concurrent use.
type Server struct {
	srv   *httptest.Server
	start time.Time

	mu        sync.Mutex
	gwDown    map[string]bool
	tunnelUp  bool
	updates   int
	checked   bool
	carp      string // MASTER, BACKUP, INIT or "" (no CARP)
	stopped   map[string]bool
	forbidden []string // path prefixes answered with 403
}

// New starts a firewall: all gateways online, the IPsec tunnel up, 3 updates pending, CARP master.
func New() *Server {
	s := &Server{start: time.Now(), gwDown: map[string]bool{}, tunnelUp: true, updates: 3, checked: true, carp: "MASTER",
		stopped: map[string]bool{}}
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

// Fingerprint returns the SHA-256 of the server certificate.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// Config is the device configuration to reach the server.
func (s *Server) Config() model.OPNsenseConfig {
	return model.OPNsenseConfig{Port: s.Port(), Key: Key, Secret: Secret, Fingerprint: s.Fingerprint()}
}

// SetGateway takes a gateway (WAN_TELEKOM, WAN_LTE) down or up.
func (s *Server) SetGateway(name string, down bool) {
	s.mu.Lock()
	s.gwDown[name] = down
	s.mu.Unlock()
}

// SetTunnel connects or disconnects the IPsec tunnel "Filiale Süd".
func (s *Server) SetTunnel(up bool) { s.mu.Lock(); s.tunnelUp = up; s.mu.Unlock() }

// SetUpdates sets the number of pending updates; checked false means no update check ran yet.
func (s *Server) SetUpdates(n int, checked bool) {
	s.mu.Lock()
	s.updates, s.checked = n, checked
	s.mu.Unlock()
}

// SetCARP sets the CARP state of all VIPs ("" = no CARP configured).
func (s *Server) SetCARP(state string) { s.mu.Lock(); s.carp = state; s.mu.Unlock() }

// SetServiceStopped stops or starts a service (unbound, openvpn, …).
func (s *Server) SetServiceStopped(name string, stopped bool) {
	s.mu.Lock()
	s.stopped[name] = stopped
	s.mu.Unlock()
}

// Forbid answers requests below a path prefix with 403 (missing privilege).
func (s *Server) Forbid(prefix string) {
	s.mu.Lock()
	s.forbidden = append(s.forbidden, prefix)
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if k, sec, ok := r.BasicAuth(); !ok || k != Key || sec != Secret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"status": "401", "message": "Authentication Failed"})
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.forbidden {
		if strings.HasPrefix(r.URL.Path, f) {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]string{"status": "403", "message": "Forbidden"})
			return
		}
	}
	up := time.Since(s.start) + 23*24*time.Hour + 4*time.Hour + 12*time.Minute
	t := time.Since(s.start).Seconds()
	switch r.URL.Path {
	case "/api/diagnostics/system/system_information":
		writeJSON(w, map[string]any{"name": "fw-filiale.kunde.local",
			"versions": []string{"OPNsense 25.7.6-amd64", "FreeBSD 14.3-RELEASE-p4", "OpenSSL 3.0.17"},
			"updates":  "Click to view pending updates."})
	case "/api/diagnostics/system/system_time":
		days := int(up.Hours() / 24)
		writeJSON(w, map[string]any{"uptime": strconv.Itoa(days) + " days, " + clock(up-time.Duration(days)*24*time.Hour),
			"datetime": time.Now().Format("Mon Jan 2 15:04:05 MST 2006"),
			"boottime": time.Now().Add(-up).Format("Mon Jan 2 15:04:05 MST 2006"), "loadavg": "0.21, 0.18, 0.15"})
	case "/api/diagnostics/system/system_resources":
		total := 8.0 * (1 << 30)
		used := total * (0.34 + 0.02*math.Sin(t/600))
		writeJSON(w, map[string]any{"memory": map[string]any{"total": strconv.FormatFloat(total, 'f', 0, 64), "total_frmt": "8192 MB",
			"used": int64(used), "used_frmt": strconv.Itoa(int(used/(1<<20))) + " MB"}})
	case "/api/diagnostics/system/system_disk":
		writeJSON(w, map[string]any{"devices": []map[string]any{
			{"device": "zroot/ROOT/default", "type": "zfs", "blocks": "107G", "used": "4.1G", "available": "103G", "used_pct": 4, "mountpoint": "/"},
			{"device": "devfs", "type": "devfs", "blocks": "1.0K", "used": "1.0K", "available": "0", "used_pct": 100, "mountpoint": "/dev"}}})
	case "/api/diagnostics/system/system_temperature":
		writeJSON(w, []map[string]any{
			{"device": "dev.cpu.0.temperature", "device_seq": 0, "temperature": strconv.FormatFloat(46+2*math.Sin(t/900), 'f', 1, 64), "type": "cpu", "type_translated": "CPU"},
			{"device": "dev.cpu.1.temperature", "device_seq": 1, "temperature": "45.0", "type": "cpu", "type_translated": "CPU"}})
	case "/api/core/firmware/status":
		if !s.checked {
			writeJSON(w, map[string]any{"status": "none", "status_msg": "Firmware status requires to check for update first to provide more information.",
				"product": map[string]any{"product_version": "25.7.6"}})
			return
		}
		pk := map[string]any{}
		names := []string{"opnsense", "openssl", "unbound", "py311-cryptography", "curl"}
		for i := 0; i < s.updates && i < len(names); i++ {
			pk[names[i]] = map[string]string{"name": names[i], "old": "1.0", "new": "1.1", "reason": "upgrade"}
		}
		st, msg, latest := "none", "There are no updates available on the selected mirror.", "25.7.6"
		if s.updates > 0 {
			st, msg, latest = "update", "There are "+strconv.Itoa(s.updates)+" updates available, total download size is 42.1MiB.", "25.7.7"
		}
		writeJSON(w, map[string]any{"status": st, "status_msg": msg, "status_reboot": "0", "all_packages": pk, "all_sets": map[string]any{},
			"product": map[string]any{"product_version": "25.7.6", "product_latest": latest, "product_name": "OPNsense"}})
	case "/api/routes/gateway/status":
		gw := func(name, addr, monitor string, delay float64) map[string]any {
			st, word := "none", "Online"
			if s.gwDown[name] {
				st, word = "down", "Offline"
			}
			return map[string]any{"name": name, "address": addr, "status": st, "status_translated": word,
				"loss": "0.0 %", "delay": strconv.FormatFloat(delay, 'f', 1, 64) + " ms", "stddev": "0.4 ms", "monitor": monitor}
		}
		writeJSON(w, map[string]any{"status": "ok", "items": []map[string]any{
			gw("WAN_TELEKOM", "62.153.10.1", "1.1.1.1", 9.8+1.5*math.Sin(t/300)),
			gw("WAN_LTE", "10.64.64.64", "8.8.8.8", 38+6*math.Sin(t/420))}})
	case "/api/ipsec/sessions/search_phase1":
		row := map[string]any{"ikeid": "1", "name": "con1", "phase1desc": "Filiale Süd", "connected": s.tunnelUp, "remote-host": "84.170.20.30"}
		if s.tunnelUp {
			row["install-time"] = 1800 + int(t)%1800
			row["bytes-in"], row["bytes-out"] = 8.2e9+t*4e4, 3.1e9+t*1.5e4
		}
		writeJSON(w, map[string]any{"total": 1, "rowCount": 1, "current": 1, "rows": []any{row}})
	case "/api/wireguard/service/show":
		writeJSON(w, map[string]any{"total": 3, "rowCount": 3, "current": 1, "rows": []any{
			map[string]any{"type": "interface", "if": "wg0", "name": "Standorte", "ifname": "Standorte", "peer-status": "offline"},
			map[string]any{"type": "peer", "if": "wg0", "ifname": "Standorte", "name": "Lager Ost", "public-key": "aB3x", "endpoint": "91.2.3.4:51820",
				"latest-handshake": time.Now().Add(-40 * time.Second).Unix(), "latest-handshake-age": 40, "peer-status": "online"},
			map[string]any{"type": "peer", "if": "wg0", "ifname": "Standorte", "name": "Notebook GF", "public-key": "cD4y", "endpoint": "(none)",
				"latest-handshake-age": nil, "peer-status": "offline"}}})
	case "/api/diagnostics/interface/get_vip_status":
		rows := []any{}
		if s.carp != "" {
			for _, v := range [][3]string{{"LAN", "1", "10.20.0.1/24"}, {"WAN", "2", "62.153.10.5/29"}} {
				rows = append(rows, map[string]any{"interface": v[0], "vhid": v[1], "advbase": "1", "advskew": "0", "subnet": v[2],
					"status": s.carp, "status_txt": s.carp, "mode": "carp", "vhid_txt": v[1]})
			}
		}
		writeJSON(w, map[string]any{"total": len(rows), "rowCount": len(rows), "current": 1, "rows": rows,
			"carp": map[string]any{"demotion": "0", "allow": "1", "maintenancemode": false}})
	case "/api/core/service/search":
		rows := []any{}
		for _, sv := range [][2]string{{"configd", "System Configuration Daemon"}, {"unbound", "Unbound DNS"}, {"dhcpd", "DHCPv4-Server"},
			{"openvpn", "OpenVPN"}, {"ntpd", "Network Time Daemon"}, {"strongswan", "IPsec VPN"}, {"wireguard", "WireGuard"}} {
			running := 1
			if s.stopped[sv[0]] {
				running = 0
			}
			rows = append(rows, map[string]any{"id": sv[0], "name": sv[0], "description": sv[1], "running": running, "locked": 0})
		}
		writeJSON(w, map[string]any{"total": len(rows), "rowCount": len(rows), "current": 1, "rows": rows})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"errorMessage": "Endpoint not found"})
	}
}

func clock(d time.Duration) string {
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	return pad(h) + ":" + pad(m) + ":" + pad(sec)
}

func pad(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
