// Package fakeunifi simulates the internal API of a UniFi OS console (a Dream Machine Pro
// with UniFi Network and Protect) for tests and the demo: login, sites, devices, health,
// sysinfo and the Protect bootstrap. It must not import core.
package fakeunifi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Login of the simulated local user.
const (
	User     = "monitoring"
	Password = "nur-anzeigen"
	token    = "fake-unifi-token"
)

// Server is the fake console.
type Server struct {
	mu          sync.Mutex
	srv         *httptest.Server
	apOffline   bool // "AP Lager" lost its connection three hours ago
	camOffline  bool // camera "Lager" is disconnected
	diskHealthy bool
	noProtect   bool
	logins      int
}

// New starts the console: one access point and one camera are offline, one camera does not
// record although it should; the disk is fine.
func New() *Server {
	s := &Server{apOffline: true, camOffline: true, diskHealthy: true}
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

// Fingerprint returns the SHA-256 fingerprint of the certificate.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// Config returns a working configuration.
func (s *Server) Config() model.UniFiConfig {
	return model.UniFiConfig{Port: s.Port(), Username: User, Password: Password, Fingerprint: s.Fingerprint()}
}

// SetDiskHealthy sets the health of the recording disk.
func (s *Server) SetDiskHealthy(ok bool) { s.mu.Lock(); s.diskHealthy = ok; s.mu.Unlock() }

// SetProtect switches UniFi Protect on or off (off: the bootstrap answers 404).
func (s *Server) SetProtect(on bool) { s.mu.Lock(); s.noProtect = !on; s.mu.Unlock() }

// Logins returns how often the client logged in.
func (s *Server) Logins() int { s.mu.Lock(); defer s.mu.Unlock(); return s.logins }

// Expire drops the session, as a console does after a restart.
func (s *Server) Expire() { s.mu.Lock(); s.logins = -s.logins - 1000; s.mu.Unlock() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func data(v ...any) map[string]any {
	return map[string]any{"meta": map[string]string{"rc": "ok"}, "data": v}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	apOffline, camOffline, diskHealthy, noProtect, expired := s.apOffline, s.camOffline, s.diskHealthy, s.noProtect, s.logins < 0
	s.mu.Unlock()
	if r.URL.Path == "/api/auth/login" {
		var in struct{ Username, Password string }
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil || in.Username != User || in.Password != Password {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]string{"code": "AUTHENTICATION_FAILED_INVALID_CREDENTIALS", "message": "Invalid username or password"})
			return
		}
		s.mu.Lock()
		if s.logins < 0 {
			s.logins = -s.logins - 1000
		}
		s.logins++
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: token, Path: "/", HttpOnly: true, Secure: true})
		w.Header().Set("X-CSRF-Token", "fake-csrf")
		writeJSON(w, map[string]any{"username": User, "isOwner": false})
		return
	}
	if ck, err := r.Cookie("TOKEN"); err != nil || ck.Value != token || expired {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	now := time.Now()
	sec := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	switch r.URL.Path {
	case "/proxy/network/api/self/sites":
		writeJSON(w, data(map[string]any{"name": "default", "desc": "Default", "_id": "5f1"}))
	case "/proxy/network/api/s/default/stat/sysinfo":
		writeJSON(w, data(map[string]any{"version": "9.0.114", "update_available": false, "name": "Default"}))
	case "/proxy/network/api/s/default/stat/health":
		writeJSON(w, data(
			map[string]any{"subsystem": "wan", "status": "ok", "wan_ip": "87.123.45.67", "isp_name": "Deutsche Telekom AG"},
			map[string]any{"subsystem": "www", "status": "ok", "latency": 11, "drops": 1},
			map[string]any{"subsystem": "wlan", "status": "ok", "num_ap": 3},
			map[string]any{"subsystem": "lan", "status": "ok", "num_sw": 1},
		))
	case "/proxy/network/api/s/default/stat/device":
		dev := func(mac, name, model, typ string, state int, version string, extra map[string]any) map[string]any {
			d := map[string]any{"mac": mac, "name": name, "model": model, "type": typ, "state": state, "version": version, "adopted": true,
				"ip": "10.0.0." + strconv.Itoa(100+int(mac[len(mac)-1])%50), "uptime": 864000 + int(mac[len(mac)-1])*3600,
				"system-stats": map[string]any{"cpu": "7.5", "mem": "41.2"}}
			for k, v := range extra {
				d[k] = v
			}
			return d
		}
		apState, apExtra := 1, map[string]any{"num_sta": 6}
		if apOffline {
			apState, apExtra = 0, map[string]any{"last_seen": sec(3*time.Hour + 12*time.Minute)}
		}
		writeJSON(w, data(
			dev("74:ac:b9:11:22:01", "UDM Pro Serverraum", "UDMPRO", "udm", 1, "4.1.13.0", map[string]any{
				"upgradable": true, "upgrade_to_firmware": "4.2.9", "general_temperature": 52.0,
				"system-stats": map[string]any{"cpu": "18.4", "mem": "61.2"},
				"wan1":         map[string]any{"up": true, "ip": "87.123.45.67", "name": "WAN"}}),
			dev("74:ac:b9:11:22:02", "Switch Büro", "US24P250", "usw", 1, "7.1.26.15869", map[string]any{"general_temperature": 41.0}),
			dev("74:ac:b9:11:22:03", "AP Empfang", "U6PRO", "uap", 1, "6.6.77.15402", map[string]any{"num_sta": 14}),
			dev("74:ac:b9:11:22:04", "AP Lager", "UAL6", "uap", apState, "6.6.77.15402", apExtra),
			dev("74:ac:b9:11:22:05", "AP Hof", "U6M", "uap", 1, "6.6.55.15189", map[string]any{"num_sta": 3, "upgradable": true, "upgrade_to_firmware": "6.6.77.15402"}),
			map[string]any{"mac": "74:ac:b9:11:22:06", "model": "U7PRO", "type": "uap", "state": 2, "adopted": false},
		))
	case "/proxy/protect/api/bootstrap":
		if noProtect {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
		cam := func(id, name, market, typ, state string, recording bool, mode string, last time.Duration) map[string]any {
			return map[string]any{"id": id, "name": name, "marketName": market, "type": typ, "state": state, "isConnected": state == "CONNECTED",
				"isRecording": recording, "isAdopted": true, "lastSeen": ms(last), "firmwareVersion": "4.73.8", "host": "10.0.0.1" + id[len(id)-1:] + "0",
				"recordingSettings": map[string]any{"mode": mode}}
		}
		lager := "CONNECTED"
		if camOffline {
			lager = "DISCONNECTED"
		}
		writeJSON(w, map[string]any{
			"nvr": map[string]any{"name": "UDM Pro Serverraum", "marketName": "UDM Pro", "version": "5.2.46", "firmwareVersion": "4.1.13",
				"isRecordingDisabled": false,
				"storageInfo": map[string]any{"hardDrives": []any{map[string]any{"name": "HDD 1", "model": "WDC WD84PURZ-85A", "serial": "WD-CA0K3X1Y",
					"size": 8001563222016, "status": "initialized", "health": map[bool]string{true: "good", false: "warning"}[diskHealthy]}}},
				"systemInfo": map[string]any{"cpu": map[string]any{"averageLoad": 0.6, "temperature": 54},
					"memory": map[string]any{"available": 1.9e6, "total": 4.0e6},
					"storage": map[string]any{"size": 7.8e12, "used": 6.95e12, "available": 0.85e12, "isRecycling": true, "type": "hdd",
						"devices": []any{map[string]any{"model": "WDC WD84PURZ-85A", "size": 8001563222016, "healthy": diskHealthy}}}}},
			"cameras": []any{
				cam("cam0000000000000000000001", "Einfahrt", "G4 Bullet", "UVC G4 Bullet", "CONNECTED", true, "always", time.Second),
				cam("cam0000000000000000000002", "Empfang", "G5 Dome", "UVC G5 Dome", "CONNECTED", true, "detections", time.Second),
				cam("cam0000000000000000000003", "Lager", "G4 Instant", "UVC G4 Instant", lager, false, "always", 2*time.Hour+5*time.Minute),
				cam("cam0000000000000000000004", "Parkplatz", "G5 Turret Ultra", "UVC G5 Turret Ultra", "CONNECTED", false, "always", time.Second),
				cam("cam0000000000000000000005", "Klingel", "G4 Doorbell Pro", "UVC G4 Doorbell Pro", "CONNECTED", true, "detections", time.Second),
				map[string]any{"id": "cam0000000000000000000009", "name": "Neu", "state": "CONNECTED", "isAdopted": false},
			},
			"lights": []any{}, "sensors": []any{},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
