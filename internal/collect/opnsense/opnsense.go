// Package opnsense polls an OPNsense firewall over its REST API (HTTP basic auth with an API
// key and secret of a user with read rights): system, firmware updates, gateways (WAN links
// and their monitoring), IPsec and WireGuard tunnels, CARP high availability and the
// services. Only GET requests are sent; nothing is changed on the firewall.
package opnsense

import (
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the HTTPS port of the web interface.
const DefaultPort = 443

const (
	requestTimeout = 20 * time.Second
	maxBody        = 8 << 20
)

// Check groups.
const (
	groupSystem   = "System"
	groupGateways = "Gateways"
	groupVPN      = "VPN"
	groupHA       = "Hochverfügbarkeit"
	groupServices = "Dienste"
	groupUpdates  = "Updates"
)

// Collector polls firewalls; it keeps an HTTP client per device.
type Collector struct {
	mu   sync.Mutex
	now  func() time.Time
	devs map[string]*client
}

// New returns a Collector.
func New() *Collector { return &Collector{now: time.Now, devs: map[string]*client{}} }

// SetClock replaces the clock (tests).
func (c *Collector) SetClock(now func() time.Time) { c.now = now }

// Forget drops the client of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl := c.devs[deviceID]; cl != nil {
		cl.http.CloseIdleConnections()
	}
	delete(c.devs, deviceID)
}

type client struct {
	sig, base, key, secret string
	http                   *http.Client
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

func signature(host string, cfg model.OPNsenseConfig) string {
	return strings.Join([]string{host, strconv.Itoa(port(cfg.Port)), cfg.Key, cfg.Secret, normFingerprint(cfg.Fingerprint)}, "\x00")
}

func newClient(host string, cfg model.OPNsenseConfig) *client {
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
	return &client{sig: signature(host, cfg), base: "https://" + net.JoinHostPort(host, strconv.Itoa(port(cfg.Port))),
		key: cfg.Key, secret: cfg.Secret, http: &http.Client{Transport: tr, Timeout: requestTimeout}}
}

func (c *Collector) client(d model.Device) *client {
	cfg := model.OPNsenseConfig{}
	if d.OPNsense != nil {
		cfg = *d.OPNsense
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := c.devs[d.ID]
	if cl == nil || cl.sig != signature(d.Host, cfg) {
		if cl != nil {
			cl.http.CloseIdleConnections()
		}
		cl = newClient(d.Host, cfg)
		c.devs[d.ID] = cl
	}
	return cl
}

// apiError is a non-2xx answer.
type apiError struct{ status int }

func (e *apiError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "API-Schlüssel oder -Geheimnis abgelehnt"
	case http.StatusForbidden:
		return "Keine Berechtigung"
	case http.StatusNotFound:
		return "Nicht vorhanden"
	}
	return fmt.Sprintf("OPNsense antwortet mit HTTP %d", e.status)
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
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck der Firewall übernehmen (" + s + ")")
	case strings.Contains(s, "connection refused"):
		return errors.New("Verbindung abgelehnt – stimmen Adresse und Port der Weboberfläche?")
	case strings.Contains(s, "Client.Timeout") || strings.Contains(s, "i/o timeout"):
		return errors.New("Zeitüberschreitung – ist die Weboberfläche vom Monitoring aus erreichbar (Firewall-Regel)?")
	}
	return err
}

func (cl *client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.base+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cl.key, cl.secret)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{status: resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unerwartete Antwort von %s: %w", path, err)
	}
	return nil
}

// num reads a JSON number or a number inside a string ("1.2 ms", "0.0 %", "1234"); ok is
// false for "N/A", "~" and other text.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		m := numRe.FindString(strings.ReplaceAll(x, ",", "."))
		if m == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(m, 64)
		return f, err == nil
	}
	return 0, false
}

var numRe = regexp.MustCompile(`-?\d+(\.\d+)?`)

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x == "1" || strings.EqualFold(x, "true")
	}
	return false
}

type obj = map[string]any

// rows reads the "rows" (search grids) or "items" list of a response.
func rows(o obj) []obj {
	var list []any
	if r, ok := o["rows"].([]any); ok {
		list = r
	} else if r, ok := o["items"].([]any); ok {
		list = r
	}
	out := make([]obj, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Collect polls a firewall. The error is set only when it does not answer or rejects the key;
// parts the key may not read become one warning (opn:perm).
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	return collect(ctx, c.client(d), c.now())
}

// poll is one collection.
type poll struct {
	ctx     context.Context
	cl      *client
	now     time.Time
	checks  []model.Check
	missing []string // parts without permission
	failed  []string // parts that could not be read
}

func (p *poll) add(ck model.Check) { p.checks = append(p.checks, ck) }

// read GETs a part; a missing permission is noted, a missing endpoint (older version,
// plugin not installed) skipped. ok is false when there is nothing to evaluate.
func (p *poll) read(path, part string, out any) bool {
	err := p.cl.get(p.ctx, path, out)
	switch {
	case err == nil:
		return true
	case status(err) == http.StatusForbidden:
		p.missing = append(p.missing, part)
	case status(err) == http.StatusNotFound:
	default:
		p.failed = append(p.failed, part+": "+err.Error())
	}
	return false
}

func collect(ctx context.Context, cl *client, now time.Time) ([]model.Check, error) {
	p := &poll{ctx: ctx, cl: cl, now: now}
	var info obj
	if err := cl.get(ctx, "/api/diagnostics/system/system_information", &info); err != nil {
		if status(err) == http.StatusForbidden {
			return nil, errors.New("Keine Berechtigung für den Systemstatus – dem API-Benutzer das Recht „Dashboard (all)“ bzw. „Diagnostics: System Activity“ geben")
		}
		return nil, err
	}
	p.system(info)
	p.firmware()
	p.gateways()
	p.ipsec()
	p.wireguard()
	p.carp()
	p.services()
	switch {
	case len(p.failed) > 0:
		p.add(model.Check{Key: "opn:perm", Name: "API-Abfrage", Group: groupSystem, State: model.StateUnknown,
			Message: strings.Join(p.failed, "; ")})
	case len(p.missing) > 0:
		slices.Sort(p.missing)
		p.add(model.Check{Key: "opn:perm", Name: "API-Abfrage", Group: groupSystem, State: model.StateWarn,
			Message: "Keine Leserechte für: " + strings.Join(p.missing, ", "),
			Facts:   map[string]string{"Abhilfe": "In OPNsense unter System → Zugang → Benutzer dem API-Benutzer (bzw. seiner Gruppe) die fehlenden Rechte geben – z. B. „Status: Gateways“, „VPN: IPsec: Status“, „VPN: WireGuard“, „Status: CARP“, „System: Firmware“, „Status: Services“."}})
	default:
		p.add(model.Check{Key: "opn:perm", Name: "API-Abfrage", Group: groupSystem, State: model.StateOK, Message: "Alle Bereiche lesbar"})
	}
	return p.checks, nil
}

// uptimeRe matches "12 days, 03:04:05", "1 day, 03:04:05" and "03:04:05".
var uptimeRe = regexp.MustCompile(`^(?:(\d+)\s+days?,\s*)?(\d+):(\d{2}):(\d{2})`)

func parseUptime(s string) (float64, bool) {
	m := uptimeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	d, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	se, _ := strconv.Atoi(m[4])
	return float64(d*86400 + h*3600 + mi*60 + se), true
}

func (p *poll) system(info obj) {
	ck := model.Check{Key: "opn:system", Name: "System", Group: groupSystem, State: model.StateOK, Facts: map[string]string{}}
	version := ""
	if vs, ok := info["versions"].([]any); ok {
		for i, v := range vs {
			s := str(v)
			switch {
			case i == 0:
				version = s
				ck.Facts["Version"] = s
			case strings.HasPrefix(s, "FreeBSD"):
				ck.Facts["Kernel"] = s
			case strings.HasPrefix(s, "OpenSSL"):
				ck.Facts["OpenSSL"] = s
			case strings.HasPrefix(s, "Licensed"):
				ck.Facts["Lizenz"] = s
			}
		}
	}
	if n := str(info["name"]); n != "" {
		ck.Facts["Name"] = n
	}
	parts := []string{}
	if version != "" {
		parts = append(parts, version)
	}
	var t obj
	if p.read("/api/diagnostics/system/system_time", "Systemzeit", &t) {
		if up, ok := parseUptime(str(t["uptime"])); ok {
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "uptime", Value: up, Unit: "s"})
			parts = append(parts, "läuft seit "+fmtDuration(time.Duration(up)*time.Second))
		}
		if la := str(t["loadavg"]); la != "" && la != "N/A" {
			ck.Facts["Last"] = la
			if l1, ok := num(strings.Split(la, ",")[0]); ok {
				ck.Metrics = append(ck.Metrics, model.Metric{Name: "cpu.load1", Value: l1})
			}
		}
		if b := str(t["boottime"]); b != "" {
			ck.Facts["Gestartet"] = b
		}
	}
	ck.Message = strings.Join(parts, " · ")
	p.add(ck)

	var res obj
	if p.read("/api/diagnostics/system/system_resources", "Systemressourcen", &res) {
		if mem, ok := res["memory"].(map[string]any); ok {
			total, okT := num(mem["total"])
			used, okU := num(mem["used"])
			if okT && okU && total > 0 {
				pct := used / total * 100
				p.add(model.Check{Key: "opn:mem", Name: "Arbeitsspeicher", Group: groupSystem, State: model.StateOK,
					Message: fmtBytes(used) + " von " + fmtBytes(total) + " belegt (" + fmtPct(pct) + ")",
					Metrics: []model.Metric{{Name: "mem.usage", Value: round2(pct), Unit: "%"}, {Name: "mem.used", Value: used, Unit: "B"},
						{Name: "mem.total", Value: total, Unit: "B"}}})
			}
		}
	}
	var disk obj
	if p.read("/api/diagnostics/system/system_disk", "Datenträger", &disk) {
		if devs, ok := disk["devices"].([]any); ok {
			for _, x := range devs {
				dv, _ := x.(map[string]any)
				if str(dv["mountpoint"]) != "/" {
					continue
				}
				if pct, ok := num(dv["used_pct"]); ok {
					ck := model.Check{Key: "opn:disk", Name: "Datenträger /", Group: "Speicher", State: model.StateOK,
						Message: fmtPct(pct) + " belegt", Metrics: []model.Metric{{Name: "disk.usage", Instance: "/", Value: pct, Unit: "%"}},
						Facts: map[string]string{"Gerät": str(dv["device"]), "Typ": str(dv["type"])}}
					if u, a := str(dv["used"]), str(dv["available"]); u != "" && a != "" {
						ck.Message = fmtPct(pct) + " belegt (" + u + " belegt, " + a + " frei)"
					}
					p.add(ck)
				}
				break
			}
		}
	}
	var temps []any
	if p.read("/api/diagnostics/system/system_temperature", "Temperaturen", &temps) {
		best, name := -1.0, ""
		for _, x := range temps {
			tm, _ := x.(map[string]any)
			if v, ok := num(tm["temperature"]); ok && v > best {
				best, name = v, str(tm["device"])
			}
		}
		if best >= 0 {
			p.add(model.Check{Key: "opn:temp", Name: "Temperatur", Group: "Sensoren", State: model.StateOK,
				Message: fmtNum(best, 0) + " °C (" + name + ")", Facts: map[string]string{"Sensor": name},
				Metrics: []model.Metric{{Name: "temp", Instance: "CPU", Value: best, Unit: "°C"}}})
		}
	}
}

func (p *poll) firmware() {
	var st obj
	if !p.read("/api/core/firmware/status", "Firmware", &st) {
		return
	}
	ck := model.Check{Key: "updates", Name: "Firmware-Updates", Group: groupUpdates, State: model.StateOK, Facts: map[string]string{}}
	product, _ := st["product"].(map[string]any)
	installed := str(product["product_version"])
	if installed != "" {
		ck.Facts["Installiert"] = installed
	}
	if l := str(product["product_latest"]); l != "" && l != installed {
		ck.Facts["Neueste Version"] = l
	}
	n := 0
	if pk, ok := st["all_packages"].(map[string]any); ok {
		n = len(pk)
	}
	sets := 0
	if s, ok := st["all_sets"].(map[string]any); ok {
		sets = len(s)
	}
	if truthy(st["status_reboot"]) {
		ck.Facts["Neustart nach dem Update"] = "ja"
	}
	msg := str(st["status_msg"])
	switch str(st["status"]) {
	case "update":
		ck.State = model.StateWarn
		ck.Message = fmt.Sprintf("%d %s verfügbar", n, plural(n, "Update", "Updates"))
		if l := ck.Facts["Neueste Version"]; l != "" {
			ck.Message += " (" + l + ")"
		}
		ck.Metrics = []model.Metric{{Name: "updates.pending", Value: float64(n)}}
	case "upgrade":
		ck.Message = "Neue Hauptversion verfügbar"
		if v := str(st["upgrade_major_version"]); v != "" {
			ck.Message += " (" + v + ")"
		}
		ck.Metrics = []model.Metric{{Name: "updates.pending", Value: float64(max(sets, 1))}}
	case "error":
		ck.State = model.StateWarn
		ck.Message = "Updatesuche fehlgeschlagen"
		if msg != "" {
			ck.Facts["Meldung"] = msg
		}
	default:
		if strings.Contains(msg, "check for update") {
			ck.State, ck.Message = model.StateUnknown, "Noch nicht nach Updates gesucht – in OPNsense unter System → Firmware → Status „Nach Updates suchen“ (oder als Cron-Job)"
		} else {
			ck.Message = "Aktuell"
			ck.Metrics = []model.Metric{{Name: "updates.pending", Value: 0}}
		}
	}
	if installed != "" && ck.State != model.StateUnknown {
		ck.Message = installed + " · " + ck.Message
	}
	p.add(ck)
}

func (p *poll) gateways() {
	var res obj
	if !p.read("/api/routes/gateway/status", "Gateways", &res) {
		return
	}
	for _, g := range rows(res) {
		name := str(g["name"])
		if name == "" {
			continue
		}
		ck := model.Check{Key: "opn:gw:" + name, Name: "Gateway " + name, Group: groupGateways, State: model.StateOK, Facts: map[string]string{}}
		for k, label := range map[string]string{"address": "Adresse", "monitor": "Überwacht über", "status_translated": "Status"} {
			if s := str(g[k]); s != "" && s != "~" {
				ck.Facts[label] = s
			}
		}
		st := str(g["status"])
		delay, okD := num(g["delay"])
		loss, okL := num(g["loss"])
		switch st {
		case "none", "":
			ck.Message = "Online"
		case "force_down":
			ck.Message = "manuell abgeschaltet"
		case "down":
			ck.State, ck.Message = model.StateCrit, "Offline"
		default: // loss, delay, delay+loss
			ck.State, ck.Message = model.StateWarn, firstNonEmpty(str(g["status_translated"]), st)
		}
		if okD && st != "down" {
			ck.Message += " · " + fmtNum(delay, 1) + " ms"
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "gw.latency", Instance: name, Value: round2(delay), Unit: "ms"})
		}
		if okL {
			if loss > 0 {
				ck.Message += " · Verlust " + fmtNum(loss, 0) + " %"
			}
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "gw.loss", Instance: name, Value: round2(loss), Unit: "%"})
		}
		p.add(ck)
	}
}

func (p *poll) ipsec() {
	var res obj
	if !p.read("/api/ipsec/sessions/search_phase1", "IPsec", &res) {
		return
	}
	for _, r := range rows(res) {
		name := firstNonEmpty(str(r["phase1desc"]), str(r["name"]), str(r["ikeid"]))
		if name == "" {
			continue
		}
		ck := model.Check{Key: "opn:ipsec:" + name, Name: "IPsec " + name, Group: groupVPN, State: model.StateOK, Facts: map[string]string{}}
		if s := str(r["remote-host"]); s != "" {
			ck.Facts["Gegenstelle"] = s
		}
		if !truthy(r["connected"]) {
			ck.State, ck.Message = model.StateCrit, "getrennt"
			p.add(ck)
			continue
		}
		ck.Message = "verbunden"
		if sec, ok := num(r["install-time"]); ok && sec > 0 {
			ck.Message += " · Schlüssel seit " + fmtDuration(time.Duration(sec)*time.Second)
		}
		in, okI := num(r["bytes-in"])
		out, okO := num(r["bytes-out"])
		if okI && okO {
			ck.Facts["Übertragen"] = "↓ " + fmtBytes(in) + " · ↑ " + fmtBytes(out)
		}
		p.add(ck)
	}
}

func (p *poll) wireguard() {
	var res obj
	if !p.read("/api/wireguard/service/show", "WireGuard", &res) {
		return
	}
	for _, r := range rows(res) {
		if str(r["type"]) != "peer" {
			continue
		}
		name := firstNonEmpty(str(r["name"]), str(r["public-key"]))
		if name == "" {
			continue
		}
		inst := firstNonEmpty(str(r["ifname"]), str(r["if"]))
		ck := model.Check{Key: "opn:wg:" + inst + "/" + name, Name: "WireGuard " + name, Group: groupVPN, State: model.StateOK, Facts: map[string]string{}}
		if inst != "" {
			ck.Facts["Instanz"] = inst
		}
		if s := str(r["endpoint"]); s != "" && s != "(none)" {
			ck.Facts["Gegenstelle"] = s
		}
		age, okAge := num(r["latest-handshake-age"])
		switch str(r["peer-status"]) {
		case "online":
			ck.Message = "verbunden"
			if okAge {
				ck.Message += " · Handshake vor " + fmtDuration(time.Duration(age)*time.Second)
			}
		case "stale":
			ck.State = model.StateWarn
			ck.Message = "keine Verbindung"
			if okAge {
				ck.Message += " · letzter Handshake vor " + fmtDuration(time.Duration(age)*time.Second)
			}
		default:
			ck.Message = "noch nie verbunden"
		}
		p.add(ck)
	}
}

func (p *poll) carp() {
	var res obj
	if !p.read("/api/diagnostics/interface/get_vip_status", "CARP", &res) {
		return
	}
	count := map[string]int{}
	facts := map[string]string{}
	n := 0
	for _, r := range rows(res) {
		if str(r["mode"]) != "carp" {
			continue
		}
		n++
		st := strings.ToUpper(str(r["status"]))
		count[st]++
		facts[firstNonEmpty(str(r["interface"]), "VIP")+" (VHID "+str(r["vhid"])+")"] = firstNonEmpty(str(r["status_txt"]), st)
	}
	if n == 0 {
		return
	}
	ck := model.Check{Key: "opn:carp", Name: "Hochverfügbarkeit (CARP)", Group: groupHA, State: model.StateOK, Facts: facts}
	vips := fmt.Sprintf("%d %s", n, plural(n, "virtuelle IP", "virtuelle IPs"))
	switch {
	case count["INIT"] > 0:
		ck.State, ck.Message = model.StateCrit, fmt.Sprintf("%d %s im Zustand INIT – Schnittstelle ohne Link?", count["INIT"], plural(count["INIT"], "virtuelle IP", "virtuelle IPs"))
	case count["MASTER"] > 0 && count["BACKUP"] > 0:
		ck.State, ck.Message = model.StateCrit, fmt.Sprintf("Uneinheitlich: %d Master, %d Backup – die Firewalls teilen sich die Adressen", count["MASTER"], count["BACKUP"])
	case count["DISABLED"] > 0:
		ck.State, ck.Message = model.StateWarn, "CARP abgeschaltet oder im Wartungsmodus"
	case count["MASTER"] == n:
		ck.Message = "Master · " + vips
	case count["BACKUP"] == n:
		ck.Message = "Backup · " + vips
	default:
		ck.State, ck.Message = model.StateUnknown, "Zustand unklar · "+vips
	}
	p.add(ck)
}

func (p *poll) services() {
	var res obj
	if !p.read("/api/core/service/search", "Dienste", &res) {
		return
	}
	for _, r := range rows(res) {
		id := firstNonEmpty(str(r["id"]), str(r["name"]))
		if id == "" {
			continue
		}
		label := firstNonEmpty(str(r["description"]), str(r["name"]))
		ck := model.Check{Key: "opn:svc:" + id, Name: "Dienst " + label, Group: groupServices, State: model.StateOK, Message: "läuft",
			Facts: map[string]string{"Dienst": str(r["name"])}}
		if !truthy(r["running"]) {
			ck.State, ck.Message = model.StateCrit, "gestoppt"
		}
		p.add(ck)
	}
}

// TestResult is what Test found.
type TestResult struct {
	Version  string   `json:"version"`
	Name     string   `json:"name"`
	Gateways int      `json:"gateways"`
	Tunnels  int      `json:"tunnels"`
	Services int      `json:"services"`
	Missing  []string `json:"missing"` // parts the key may not read
}

// Test reads the firewall once.
func Test(ctx context.Context, host string, cfg model.OPNsenseConfig) (TestResult, error) {
	cl := newClient(host, cfg)
	defer cl.http.CloseIdleConnections()
	checks, err := collect(ctx, cl, time.Now())
	if err != nil {
		return TestResult{}, err
	}
	r := TestResult{Missing: []string{}}
	for _, ck := range checks {
		switch {
		case ck.Key == "opn:system":
			r.Version, r.Name = ck.Facts["Version"], ck.Facts["Name"]
		case strings.HasPrefix(ck.Key, "opn:gw:"):
			r.Gateways++
		case strings.HasPrefix(ck.Key, "opn:ipsec:") || strings.HasPrefix(ck.Key, "opn:wg:"):
			r.Tunnels++
		case strings.HasPrefix(ck.Key, "opn:svc:"):
			r.Services++
		case ck.Key == "opn:perm" && ck.State == model.StateWarn:
			r.Missing = strings.Split(strings.TrimPrefix(ck.Message, "Keine Leserechte für: "), ", ")
		}
	}
	return r, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func fmtNum(v float64, prec int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', prec, 64), ".", ",", 1)
}

func fmtPct(v float64) string { return fmtNum(math.Round(v), 0) + " %" }

func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 {
		return fmtNum(b, 0) + " " + units[i]
	}
	return fmtNum(b, 1) + " " + units[i]
}

func fmtDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tagen"
	case d >= 24*time.Hour:
		return "1 Tag"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	case d >= time.Minute:
		return strconv.Itoa(int(d.Minutes())) + " Min."
	}
	return strconv.Itoa(max(0, int(d.Seconds()))) + " s"
}
