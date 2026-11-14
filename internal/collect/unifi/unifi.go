// Package unifi polls a UniFi OS console (Dream Machine, Dream Router, Cloud Gateway, Cloud
// Key, UNVR …) with a local user over the internal API the UniFi web interface uses: the
// Network application (every adopted access point, switch and gateway, the internet
// connection, versions) and UniFi Protect (cameras, recording, the recording storage and its
// disks). The internal API is not documented by Ubiquiti and may change with updates; the
// collector reads only fields the UniFi interface and Home Assistant rely on.
package unifi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the HTTPS port of the console.
const DefaultPort = 443

const (
	requestTimeout = 20 * time.Second
	maxBody        = 32 << 20 // a Protect bootstrap of a large site is several MB
)

// Check groups.
const (
	groupConsole = "UniFi"
	groupDevices = "UniFi-Geräte"
	groupCameras = "Kameras"
	groupProtect = "UniFi Protect"
)

// Collector polls consoles; it keeps the logged-in session per device.
type Collector struct {
	mu   sync.Mutex
	now  func() time.Time
	devs map[string]*session
}

type session struct {
	mu       sync.Mutex
	sig      string
	base     string
	user     string
	pass     string
	http     *http.Client
	loggedIn bool
}

// New returns a Collector.
func New() *Collector { return &Collector{now: time.Now, devs: map[string]*session{}} }

// Forget drops the session of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.devs[deviceID]; s != nil {
		s.http.CloseIdleConnections()
	}
	delete(c.devs, deviceID)
}

func port(p int) int {
	if p <= 0 {
		return DefaultPort
	}
	return p
}

func normFingerprint(f string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(f)))
}

func newSession(host string, cfg model.UniFiConfig) *session {
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 2,
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if want := normFingerprint(cfg.Fingerprint); want != "" {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // verified below by pinning
		tr.TLSClientConfig.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("kein Zertifikat")
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != want {
				return errors.New("Zertifikat passt nicht zum hinterlegten Fingerabdruck")
			}
			return nil
		}
	}
	jar, _ := cookiejar.New(nil)
	return &session{sig: signature(host, cfg), base: "https://" + net.JoinHostPort(host, strconv.Itoa(port(cfg.Port))),
		user: cfg.Username, pass: cfg.Password, http: &http.Client{Transport: tr, Timeout: requestTimeout, Jar: jar}}
}

func signature(host string, cfg model.UniFiConfig) string {
	return strings.Join([]string{host, strconv.Itoa(port(cfg.Port)), cfg.Username, cfg.Password, normFingerprint(cfg.Fingerprint), cfg.Site}, "\x00")
}

type apiError struct {
	status int
	path   string
}

func (e *apiError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "Anmeldung abgelaufen oder abgelehnt"
	case http.StatusForbidden:
		return "Keine Berechtigung"
	case http.StatusNotFound:
		return "Nicht vorhanden"
	case http.StatusTooManyRequests:
		return "Zu viele Anfragen – die Konsole bremst, beim nächsten Mal wieder"
	}
	return fmt.Sprintf("Konsole antwortet mit HTTP %d", e.status)
}

func status(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	return 0
}

func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "certificate") || strings.Contains(s, "Fingerabdruck"):
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck der Konsole übernehmen (" + s + ")")
	case strings.Contains(s, "connection refused"):
		return errors.New("Verbindung abgelehnt – ist das die Adresse einer UniFi-OS-Konsole (HTTPS)?")
	case strings.Contains(s, "Client.Timeout") || strings.Contains(s, "i/o timeout"):
		return errors.New("Zeitüberschreitung – ist die Konsole vom Monitoring aus erreichbar?")
	}
	return err
}

// login signs in with the local user (UniFi OS: POST /api/auth/login sets the cookie TOKEN).
func (s *session) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"username": s.user, "password": s.pass, "rememberMe": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	low := strings.ToLower(string(b))
	switch {
	case resp.StatusCode == http.StatusOK:
		s.loggedIn = true
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("Keine UniFi-OS-Konsole (Anmeldung unter /api/auth/login fehlt) – ältere Controller ohne UniFi OS werden nicht unterstützt")
	case resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("Zu viele Anmeldeversuche – die Konsole sperrt kurz, bitte einige Minuten warten")
	case strings.Contains(low, "mfa") || strings.Contains(low, "2fa"):
		return errors.New("Der Benutzer verlangt Zwei-Faktor-Anmeldung – einen lokalen Benutzer ohne 2FA (nur lokaler Zugriff) anlegen")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest:
		return errors.New("Anmeldung abgelehnt – Benutzer und Kennwort prüfen (lokaler Benutzer der Konsole, nicht das UI-Konto)")
	}
	return fmt.Errorf("Anmeldung fehlgeschlagen (HTTP %d)", resp.StatusCode)
}

// get reads a JSON resource, logging in first and once more when the session expired.
func (s *session) get(ctx context.Context, path string, out any) error {
	for attempt := 0; ; attempt++ {
		if !s.loggedIn {
			if err := s.login(ctx); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := s.http.Do(req)
		if err != nil {
			return friendly(err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		if err != nil {
			return friendly(err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			s.loggedIn = false
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return &apiError{status: resp.StatusCode, path: path}
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("unerwartete Antwort von %s (%v)", path, err)
		}
		return nil
	}
}

// ---- Network ----

// numStr reads numbers that UniFi sends as strings ("12.3") or numbers.
type numStr float64

func (n *numStr) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*n = numStr(v)
	}
	return nil // unknown formats are left out
}

type site struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

type wan struct {
	Up   bool   `json:"up"`
	IP   string `json:"ip"`
	Name string `json:"name"`
}

type netDevice struct {
	MAC        string   `json:"mac"`
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	Type       string   `json:"type"` // uap, usw, ugw, udm, uxg …
	State      int      `json:"state"`
	Version    string   `json:"version"`
	Upgradable bool     `json:"upgradable"`
	UpgradeTo  string   `json:"upgrade_to_firmware"`
	Uptime     int64    `json:"uptime"`
	LastSeen   int64    `json:"last_seen"`
	IP         string   `json:"ip"`
	Adopted    bool     `json:"adopted"`
	NumSta     *int     `json:"num_sta"`
	Temp       *float64 `json:"general_temperature"`
	Stats      struct {
		CPU numStr `json:"cpu"`
		Mem numStr `json:"mem"`
	} `json:"system-stats"`
	Wan1 *wan `json:"wan1"`
	Wan2 *wan `json:"wan2"`
}

type health struct {
	Subsystem string   `json:"subsystem"` // wan, www, wlan, lan, vpn
	Status    string   `json:"status"`    // ok, warning, error, unknown
	WanIP     string   `json:"wan_ip"`
	ISP       string   `json:"isp_name"`
	Latency   *float64 `json:"latency"`
	Drops     int      `json:"drops"`
}

type sysinfo struct {
	Version         string `json:"version"`
	UpdateAvailable bool   `json:"update_available"`
}

type list[T any] struct {
	Data []T `json:"data"`
}

// ---- Protect ----

type camera struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Type              string `json:"type"`
	MarketName        string `json:"marketName"`
	State             string `json:"state"` // CONNECTED, DISCONNECTED, CONNECTING, UPDATING …
	IsConnected       bool   `json:"isConnected"`
	IsRecording       bool   `json:"isRecording"`
	IsAdopted         bool   `json:"isAdopted"`
	LastSeen          int64  `json:"lastSeen"` // ms
	FirmwareVersion   string `json:"firmwareVersion"`
	Host              string `json:"host"`
	RecordingSettings struct {
		Mode string `json:"mode"` // always, detections, never, schedule
	} `json:"recordingSettings"`
}

type storageDevice struct {
	Model   string  `json:"model"`
	Size    float64 `json:"size"`
	Healthy *bool   `json:"healthy"`
}

type hardDrive struct {
	Model  string `json:"model"`
	Serial string `json:"serial"`
	Health string `json:"health"`
}

type bootstrap struct {
	NVR struct {
		MarketName          string `json:"marketName"`
		Version             string `json:"version"`
		FirmwareVersion     string `json:"firmwareVersion"`
		IsRecordingDisabled bool   `json:"isRecordingDisabled"`
		StorageInfo         struct {
			HardDrives []hardDrive `json:"hardDrives"`
		} `json:"storageInfo"`
		SystemInfo struct {
			CPU struct {
				Temperature *float64 `json:"temperature"`
			} `json:"cpu"`
			Storage struct {
				Size        float64         `json:"size"`
				Used        float64         `json:"used"`
				IsRecycling bool            `json:"isRecycling"`
				Devices     []storageDevice `json:"devices"`
			} `json:"storage"`
		} `json:"systemInfo"`
	} `json:"nvr"`
	Cameras []camera `json:"cameras"`
}

// ---- Collect ----

// Collect polls one console. It returns an error when the console cannot be reached or
// rejects the login; UniFi Protect is optional.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.UniFi == nil || strings.TrimSpace(d.UniFi.Username) == "" {
		return nil, errors.New("Zugang zur UniFi-Konsole fehlt (Benutzer und Kennwort)")
	}
	if strings.TrimSpace(d.Host) == "" {
		return nil, errors.New("Host fehlt")
	}
	c.mu.Lock()
	s := c.devs[d.ID]
	if s == nil || s.sig != signature(d.Host, *d.UniFi) {
		if s != nil {
			s.http.CloseIdleConnections()
		}
		s = newSession(d.Host, *d.UniFi)
		c.devs[d.ID] = s
	}
	c.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return collect(ctx, s, d.UniFi.Site, c.now())
}

func collect(ctx context.Context, s *session, onlySite string, now time.Time) ([]model.Check, error) {
	var sites list[site]
	if err := s.get(ctx, "/proxy/network/api/self/sites", &sites); err != nil {
		switch status(err) {
		case http.StatusForbidden:
			return nil, errors.New("Der Benutzer darf UniFi Network nicht lesen – Rolle „Nur anzeigen“ für Network vergeben")
		case http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable:
			sites.Data = nil // a console without Network (UNVR): Protect only
		default:
			return nil, err
		}
	}
	var checks []model.Check
	var gateway *netDevice
	netVersion, netUpdate := "", false
	multi := len(sites.Data) > 1
	for _, st := range sites.Data {
		if onlySite != "" && !strings.EqualFold(onlySite, st.Name) && !strings.EqualFold(onlySite, st.Desc) {
			continue
		}
		var devs list[netDevice]
		if err := s.get(ctx, "/proxy/network/api/s/"+st.Name+"/stat/device", &devs); err != nil {
			return nil, err
		}
		var hs list[health]
		_ = s.get(ctx, "/proxy/network/api/s/"+st.Name+"/stat/health", &hs)
		if netVersion == "" {
			var si list[sysinfo]
			if s.get(ctx, "/proxy/network/api/s/"+st.Name+"/stat/sysinfo", &si) == nil && len(si.Data) > 0 {
				netVersion, netUpdate = si.Data[0].Version, si.Data[0].UpdateAvailable
			}
		}
		label := ""
		if multi {
			label = cmpOr(st.Desc, st.Name)
		}
		for i := range devs.Data {
			if isGateway(devs.Data[i].Type) && gateway == nil {
				gateway = &devs.Data[i]
			}
		}
		if ch, ok := wanCheck(st.Name, label, hs.Data, devs.Data); ok {
			checks = append(checks, ch)
		}
		checks = append(checks, deviceChecks(devs.Data, label)...)
	}

	var bs bootstrap
	protect := true
	if err := s.get(ctx, "/proxy/protect/api/bootstrap", &bs); err != nil {
		protect = false
		switch status(err) {
		case http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable:
			// Protect is not installed or not running
		case http.StatusForbidden, http.StatusUnauthorized:
			checks = append(checks, model.Check{Key: "unifi:protect", Name: "UniFi Protect", Group: groupProtect, State: model.StateWarn,
				Message: "Der Benutzer darf UniFi Protect nicht lesen – Rolle „Nur anzeigen“ für Protect vergeben"})
		default:
			checks = append(checks, model.Check{Key: "unifi:protect", Name: "UniFi Protect", Group: groupProtect, State: model.StateUnknown,
				Message: "Protect nicht lesbar: " + err.Error()})
		}
	}
	if protect {
		checks = append(checks, protectChecks(bs)...)
	}
	return append([]model.Check{consoleCheck(gateway, netVersion, netUpdate, bs, protect)}, checks...), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isGateway(t string) bool { return t == "udm" || t == "uxg" || t == "ugw" || t == "ucg" }

// modelNames are the product names of common console and gateway model codes.
var modelNames = map[string]string{
	"UDM": "Dream Machine", "UDMPRO": "Dream Machine Pro", "UDMPROSE": "Dream Machine Pro SE", "UDMPROMAX": "Dream Machine Pro Max",
	"UDR": "Dream Router", "UDR7": "Dream Router 7", "UDW": "Dream Wall", "UCGULTRA": "Cloud Gateway Ultra", "UCGMAX": "Cloud Gateway Max",
	"UCGFIBER": "Cloud Gateway Fiber", "UXGPRO": "Next-Gen Gateway Pro", "UXG": "Next-Gen Gateway Lite", "UXGLITE": "Next-Gen Gateway Lite",
	"UGW3": "Security Gateway", "UGW4": "Security Gateway Pro", "UCKG2": "Cloud Key Gen2", "UCKP": "Cloud Key Gen2 Plus",
	"UNVR": "Network Video Recorder", "UNVRPRO": "Network Video Recorder Pro",
}

func modelName(code string) string {
	if n, ok := modelNames[strings.ToUpper(code)]; ok {
		return n
	}
	return code
}

func consoleCheck(gw *netDevice, netVersion string, netUpdate bool, bs bootstrap, protect bool) model.Check {
	ch := model.Check{Key: "unifi:console", Name: "Konsole", Group: groupConsole, State: model.StateOK, Facts: map[string]string{}}
	name := "UniFi-Konsole"
	switch {
	case gw != nil:
		name = cmpOr(modelName(gw.Model), name)
		ch.Facts["Firmware"] = gw.Version
		if gw.Upgradable && gw.UpgradeTo != "" {
			ch.Facts["Firmware-Update"] = gw.UpgradeTo
		}
	case protect && bs.NVR.MarketName != "":
		name = bs.NVR.MarketName
		ch.Facts["Firmware"] = bs.NVR.FirmwareVersion
	}
	parts := []string{name}
	if netVersion != "" {
		parts = append(parts, "Network "+netVersion)
		ch.Facts["UniFi Network"] = netVersion
		if netUpdate {
			ch.Facts["UniFi Network"] += " (Update verfügbar)"
		}
	}
	if protect && bs.NVR.Version != "" {
		parts = append(parts, "Protect "+bs.NVR.Version)
		ch.Facts["UniFi Protect"] = bs.NVR.Version
	}
	ch.Message = strings.Join(parts, " · ")
	if gw != nil {
		if gw.Stats.CPU > 0 {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "cpu.usage", Value: float64(gw.Stats.CPU), Unit: "%"})
		}
		if gw.Stats.Mem > 0 {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "mem.usage", Value: float64(gw.Stats.Mem), Unit: "%"})
		}
		if gw.Temp != nil {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "temp", Value: *gw.Temp, Unit: "°C"})
		}
	}
	return ch
}

// wanCheck is the internet connection of a site (health subsystems wan and www, the WAN ports
// of the gateway).
func wanCheck(siteName, label string, hs []health, devs []netDevice) (model.Check, bool) {
	var w, www *health
	for i := range hs {
		switch hs[i].Subsystem {
		case "wan":
			w = &hs[i]
		case "www":
			www = &hs[i]
		}
	}
	if w == nil || w.Status == "unknown" || w.Status == "" {
		return model.Check{}, false
	}
	name := "Internet"
	if label != "" {
		name += " (" + label + ")"
	}
	ch := model.Check{Key: "unifi:wan:" + siteName, Name: name, Group: groupConsole, State: model.StateOK, Facts: map[string]string{}}
	var parts []string
	switch w.Status {
	case "ok":
		parts = append(parts, "Verbunden")
	case "warning":
		ch.State = model.StateWarn
		parts = append(parts, "WAN eingeschränkt")
	default:
		ch.State = model.StateCrit
		parts = append(parts, "WAN getrennt")
	}
	if www != nil {
		switch {
		case www.Status == "error" && ch.State == model.StateOK:
			ch.State, parts[0] = model.StateCrit, "Kein Internet"
		case www.Status == "warning" && ch.State == model.StateOK:
			ch.State, parts[0] = model.StateWarn, "Internet eingeschränkt"
		}
		if www.Latency != nil {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "unifi.latency", Value: *www.Latency, Unit: "ms"})
			parts = append(parts, fmt.Sprintf("%.0f ms", *www.Latency))
		}
		if www.Drops > 0 {
			ch.Facts["Abbrüche"] = strconv.Itoa(www.Drops)
		}
	}
	if w.WanIP != "" {
		ch.Facts["WAN-IP"] = w.WanIP
		parts = append(parts, "WAN-IP "+w.WanIP)
	}
	if w.ISP != "" {
		ch.Facts["Anbieter"] = w.ISP
	}
	for _, d := range devs {
		if !isGateway(d.Type) {
			continue
		}
		for i, wp := range []*wan{d.Wan1, d.Wan2} {
			if wp == nil {
				continue
			}
			key := fmt.Sprintf("WAN %d", i+1)
			if wp.Up {
				ch.Facts[key] = strings.TrimSpace("verbunden " + wp.IP)
				continue
			}
			ch.Facts[key] = "getrennt"
			if i == 1 && ch.State == model.StateOK {
				ch.State = model.StateWarn
				parts = append(parts, "WAN 2 getrennt (Ausfallleitung)")
			}
		}
	}
	ch.Message = strings.Join(parts, " · ")
	return ch, true
}

var deviceKind = map[string]string{"uap": "Access Point", "usw": "Switch", "ugw": "Gateway", "udm": "Gateway", "uxg": "Gateway", "ucg": "Gateway",
	"ubb": "Bridge", "uph": "Telefon", "ulte": "LTE-Backup", "usp": "Steckdose"}

var stateWord = map[int]string{0: "Getrennt", 1: "Verbunden", 2: "Wartet auf Übernahme", 4: "Firmware wird aktualisiert", 5: "Wird bereitgestellt",
	6: "Verbindung unterbrochen (Heartbeat fehlt)", 7: "Übernahme fehlgeschlagen", 9: "Übernahme fehlgeschlagen", 10: "Wird übernommen", 11: "Isoliert"}

// deviceChecks are the adopted network devices of a site.
func deviceChecks(devs []netDevice, label string) []model.Check {
	var out []model.Check
	for _, d := range devs {
		if !d.Adopted {
			continue
		}
		name := cmpOr(d.Name, d.Model+" "+d.MAC)
		if label != "" {
			name += " (" + label + ")"
		}
		kind := cmpOr(deviceKind[d.Type], "Gerät")
		ch := model.Check{Key: "unifi:dev:" + strings.ToLower(strings.ReplaceAll(d.MAC, ":", "")), Name: name, Group: groupDevices,
			State: model.StateOK, Facts: map[string]string{"Art": kind, "Modell": modelName(d.Model), "MAC": d.MAC, "Firmware": d.Version}}
		if d.IP != "" {
			ch.Facts["IP"] = d.IP
		}
		word := stateWord[d.State]
		if word == "" {
			word = fmt.Sprintf("Zustand %d", d.State)
		}
		parts := []string{word}
		switch d.State {
		case 1:
			if d.Uptime > 0 {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "uptime", Value: float64(d.Uptime), Unit: "s"})
			}
			if d.NumSta != nil {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "unifi.clients", Value: float64(*d.NumSta)})
				if d.Type == "uap" {
					parts = append(parts, fmt.Sprintf("%d %s", *d.NumSta, plural(*d.NumSta, "Client", "Clients")))
				}
			}
			if d.Stats.CPU > 0 {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "cpu.usage", Value: float64(d.Stats.CPU), Unit: "%"})
			}
			if d.Stats.Mem > 0 {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "mem.usage", Value: float64(d.Stats.Mem), Unit: "%"})
			}
			if d.Temp != nil {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "temp", Value: *d.Temp, Unit: "°C"})
			}
		case 0:
			ch.State = model.StateCrit
			if d.LastSeen > 0 {
				parts[0] += " seit " + time.Unix(d.LastSeen, 0).Local().Format("02.01. 15:04")
			}
		case 4, 5, 10:
			// updating or provisioning: offline for a short time, no alarm
		default:
			ch.State = model.StateWarn
		}
		parts = append(parts, kind+" "+modelName(d.Model))
		if d.Upgradable && d.UpgradeTo != "" {
			ch.Facts["Firmware-Update"] = d.UpgradeTo
			parts = append(parts, "Update auf "+d.UpgradeTo+" verfügbar")
		}
		ch.Message = strings.Join(parts, " · ")
		out = append(out, ch)
	}
	slices.SortFunc(out, func(a, b model.Check) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

var cameraState = map[string]string{"CONNECTED": "Verbunden", "DISCONNECTED": "Getrennt", "CONNECTING": "Verbindet", "UPDATING": "Firmware wird aktualisiert",
	"PROVISIONING": "Wird bereitgestellt", "RESETTING": "Wird zurückgesetzt"}

var recordingMode = map[string]string{"always": "Dauerhaft", "detections": "Bei Erkennung", "never": "Aus", "schedule": "Nach Zeitplan"}

func protectChecks(bs bootstrap) []model.Check {
	var out []model.Check
	nvr := bs.NVR
	st := nvr.SystemInfo.Storage
	ch := model.Check{Key: "unifi:nvr", Name: "Protect-Aufzeichnung", Group: groupProtect, State: model.StateOK, Facts: map[string]string{}}
	var parts []string
	if nvr.IsRecordingDisabled {
		ch.State = model.StateWarn
		parts = append(parts, "Aufzeichnung für alle Kameras ausgeschaltet")
	}
	if st.Size > 0 {
		pct := st.Used / st.Size * 100
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "unifi.storage_usage", Value: math.Round(pct*10) / 10, Unit: "%"})
		parts = append(parts, fmt.Sprintf("Speicher %.0f %% von %s", pct, fmtBytes(st.Size)))
		if st.IsRecycling {
			ch.Facts["Speicher"] = "voll – älteste Aufnahmen werden überschrieben (normal)"
		}
	} else {
		ch.State = model.StateCrit
		parts = append(parts, "Kein Aufnahmespeicher")
	}
	if t := nvr.SystemInfo.CPU.Temperature; t != nil {
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "temp", Value: *t, Unit: "°C"})
	}
	ch.Message = strings.Join(parts, " · ")
	out = append(out, ch)

	// disks: the storage devices with their health, details from storageInfo
	for i, dev := range st.Devices {
		dc := model.Check{Key: fmt.Sprintf("unifi:disk:%d", i+1), Name: fmt.Sprintf("Festplatte %d", i+1), Group: groupProtect,
			State: model.StateOK, Facts: map[string]string{"Modell": dev.Model}}
		if dev.Size > 0 {
			dc.Facts["Größe"] = fmtBytes(dev.Size)
		}
		if i < len(nvr.StorageInfo.HardDrives) {
			hd := nvr.StorageInfo.HardDrives[i]
			if hd.Serial != "" {
				dc.Facts["Seriennummer"] = hd.Serial
			}
			if hd.Health != "" {
				dc.Facts["Zustand laut Protect"] = hd.Health
			}
		}
		word := "In Ordnung"
		switch {
		case dev.Healthy == nil:
			dc.State, word = model.StateUnknown, "Zustand unbekannt"
		case !*dev.Healthy:
			dc.State, word = model.StateCrit, "Fehlerhaft – Festplatte tauschen"
		}
		dc.Message = strings.Join(slices.DeleteFunc([]string{word, dev.Model}, func(x string) bool { return x == "" }), " · ")
		out = append(out, dc)
	}

	cams := slices.Clone(bs.Cameras)
	slices.SortFunc(cams, func(a, b camera) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	for _, cm := range cams {
		if !cm.IsAdopted {
			continue
		}
		cc := model.Check{Key: "unifi:cam:" + strings.ToLower(cm.ID), Name: cmpOr(cm.Name, cm.MarketName), Group: groupCameras, State: model.StateOK,
			Facts: map[string]string{"Modell": cmpOr(cm.MarketName, cm.Type), "Firmware": cm.FirmwareVersion}}
		if cm.Host != "" {
			cc.Facts["IP"] = cm.Host
		}
		mode := cm.RecordingSettings.Mode
		if w := recordingMode[mode]; w != "" {
			cc.Facts["Aufzeichnung"] = w
		}
		word := cmpOr(cameraState[cm.State], cm.State)
		parts := []string{word}
		switch {
		case cm.State == "CONNECTED" || (cm.State == "" && cm.IsConnected):
			switch {
			case mode == "never":
				parts = append(parts, "Aufzeichnung aus")
			case !cm.IsRecording && !nvr.IsRecordingDisabled:
				cc.State = model.StateWarn
				parts = []string{"Nimmt nicht auf", "verbunden"}
			default:
				parts = append(parts, "nimmt auf ("+cmpOr(recordingMode[mode], mode)+")")
			}
		case cm.State == "DISCONNECTED":
			cc.State = model.StateCrit
			if cm.LastSeen > 0 {
				parts[0] += " seit " + time.UnixMilli(cm.LastSeen).Local().Format("02.01. 15:04")
			}
		case cm.State == "UPDATING" || cm.State == "PROVISIONING" || cm.State == "CONNECTING":
		default:
			cc.State = model.StateWarn
		}
		parts = append(parts, cmpOr(cm.MarketName, cm.Type))
		cc.Message = strings.Join(parts, " · ")
		out = append(out, cc)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for b >= 1000 && i < len(units)-1 {
		b /= 1000
		i++
	}
	return strings.Replace(strconv.FormatFloat(b, 'f', 1, 64), ".", ",", 1) + " " + units[i]
}

// ---- connection test ----

// TestResult is the answer of a connection test.
type TestResult struct {
	Console string `json:"console"`
	Network string `json:"network"` // version, "" = no UniFi Network
	Protect string `json:"protect"` // version, "" = no UniFi Protect
	Devices int    `json:"devices"`
	Cameras int    `json:"cameras"`
}

// Test logs in and reads Network and Protect once.
func Test(ctx context.Context, host string, cfg model.UniFiConfig) (TestResult, error) {
	s := newSession(host, cfg)
	defer s.http.CloseIdleConnections()
	checks, err := collect(ctx, s, cfg.Site, time.Now())
	if err != nil {
		return TestResult{}, err
	}
	var res TestResult
	for _, ch := range checks {
		switch {
		case ch.Key == "unifi:console":
			res.Console = strings.Split(ch.Message, " · ")[0]
			res.Network = strings.TrimSuffix(ch.Facts["UniFi Network"], " (Update verfügbar)")
			res.Protect = ch.Facts["UniFi Protect"]
		case strings.HasPrefix(ch.Key, "unifi:dev:"):
			res.Devices++
		case strings.HasPrefix(ch.Key, "unifi:cam:"):
			res.Cameras++
		}
	}
	return res, nil
}
