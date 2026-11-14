// Package redfish polls the management controller of a server (Dell iDRAC, HPE iLO, Lenovo
// XCC, Supermicro BMC …) over the Redfish REST API with a read-only user: system and power
// state, power supplies and redundancy, fans, temperatures (judged by the controller's own
// thresholds), memory and processors, and the RAID controllers with their volumes and drives.
// It needs no software on the server and works while the operating system is down.
package redfish

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the HTTPS port of the controller.
const DefaultPort = 443

const (
	requestTimeout = 20 * time.Second
	storageTTL     = 5 * time.Minute // storage trees have many resources and change rarely
	maxMembers     = 64
)

// Check groups.
const (
	groupSystem = "System"
	groupPower  = "Netzteile"
	groupCool   = "Kühlung"
	groupRAID   = "RAID"
	groupDrives = "Platten"
)

// Collector polls Redfish controllers; it caches the storage tree per device.
type Collector struct {
	mu   sync.Mutex
	now  func() time.Time
	devs map[string]*devState
}

type devState struct {
	sig       string
	cl        *client
	storage   []model.Check
	storageAt time.Time
}

// New returns a Collector.
func New() *Collector { return &Collector{now: time.Now, devs: map[string]*devState{}} }

// Forget drops the state of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.devs[deviceID]; st != nil {
		st.cl.http.CloseIdleConnections()
	}
	delete(c.devs, deviceID)
}

type client struct {
	base       string
	user, pass string
	http       *http.Client
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

func newClient(host string, cfg model.RedfishConfig) *client {
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
	return &client{base: "https://" + net.JoinHostPort(host, strconv.Itoa(port(cfg.Port))), user: cfg.Username, pass: cfg.Password,
		http: &http.Client{Transport: tr, Timeout: requestTimeout}}
}

func signature(host string, cfg model.RedfishConfig) string {
	return strings.Join([]string{host, strconv.Itoa(port(cfg.Port)), cfg.Username, cfg.Password, normFingerprint(cfg.Fingerprint)}, "\x00")
}

type apiError struct{ status int }

func (e *apiError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "Anmeldung abgelehnt – Benutzer und Kennwort prüfen (ist der Benutzer für Redfish freigegeben?)"
	case http.StatusForbidden:
		return "Keine Berechtigung – der Benutzer braucht mindestens Leserechte (Read Only / Login)"
	case http.StatusNotFound:
		return "Redfish-Ressource nicht gefunden"
	}
	return fmt.Sprintf("Controller antwortet mit HTTP %d", e.status)
}

func friendly(err error) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "Fingerabdruck"):
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck des Controllers übernehmen (" + err.Error() + ")")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("Verbindung abgelehnt – ist Redfish am Controller eingeschaltet (HTTPS-Port)?")
	case strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "i/o timeout"):
		return errors.New("Zeitüberschreitung – ist der Management-Port (iDRAC, iLO …) vom Monitoring aus erreichbar?")
	}
	return err
}

// get reads a Redfish resource by its @odata.id path.
func (cl *client) get(ctx context.Context, path string, out any) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("ungültiger Pfad %q", path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.base+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cl.user, cl.pass)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OData-Version", "4.0")
	resp, err := cl.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &apiError{status: resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("Antwort von %s nicht lesbar: %w", path, err)
	}
	return nil
}

// members reads a collection and returns the member paths.
func (cl *client) members(ctx context.Context, path string) ([]string, error) {
	var col struct {
		Members []link `json:"Members"`
	}
	if err := cl.get(ctx, path, &col); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range col.Members {
		if m.ID != "" && len(out) < maxMembers {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

type link struct {
	ID string `json:"@odata.id"`
}

type status struct {
	State        string `json:"State"`
	Health       string `json:"Health"`
	HealthRollup string `json:"HealthRollup"`
}

// num accepts numbers and null.
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	var f *float64
	if err := json.Unmarshal(b, &f); err != nil || f == nil {
		*n = num{}
		return nil
	}
	*n = num{v: *f, ok: true}
	return nil
}

type serviceRoot struct {
	Vendor   string `json:"Vendor"`
	Version  string `json:"RedfishVersion"`
	Systems  link   `json:"Systems"`
	Chassis  link   `json:"Chassis"`
	Managers link   `json:"Managers"`
}

type system struct {
	ID            string `json:"Id"`
	Manufacturer  string `json:"Manufacturer"`
	Model         string `json:"Model"`
	SerialNumber  string `json:"SerialNumber"`
	SKU           string `json:"SKU"`
	BiosVersion   string `json:"BiosVersion"`
	HostName      string `json:"HostName"`
	PowerState    string `json:"PowerState"`
	Status        status `json:"Status"`
	MemorySummary struct {
		TotalSystemMemoryGiB num    `json:"TotalSystemMemoryGiB"`
		Status               status `json:"Status"`
	} `json:"MemorySummary"`
	ProcessorSummary struct {
		Count  int    `json:"Count"`
		Model  string `json:"Model"`
		Status status `json:"Status"`
	} `json:"ProcessorSummary"`
	Storage link `json:"Storage"`
}

type chassis struct {
	ID      string `json:"Id"`
	Thermal link   `json:"Thermal"`
	Power   link   `json:"Power"`
}

type redundancy struct {
	Name   string `json:"Name"`
	Mode   string `json:"Mode"`
	Status status `json:"Status"`
}

type tempSensor struct {
	Name           string `json:"Name"`
	ReadingCelsius num    `json:"ReadingCelsius"`
	UpperNonCrit   num    `json:"UpperThresholdNonCritical"`
	UpperCrit      num    `json:"UpperThresholdCritical"`
	UpperFatal     num    `json:"UpperThresholdFatal"`
	Status         status `json:"Status"`
}

type fan struct {
	Name         string `json:"Name"`
	FanName      string `json:"FanName"` // Redfish before 2016
	Reading      num    `json:"Reading"`
	ReadingUnits string `json:"ReadingUnits"`
	Status       status `json:"Status"`
}

type thermal struct {
	Temperatures []tempSensor `json:"Temperatures"`
	Fans         []fan        `json:"Fans"`
	Redundancy   []redundancy `json:"Redundancy"`
}

type powerSupply struct {
	Name               string `json:"Name"`
	Model              string `json:"Model"`
	PowerCapacityWatts num    `json:"PowerCapacityWatts"`
	LineInputVoltage   num    `json:"LineInputVoltage"`
	Status             status `json:"Status"`
}

type power struct {
	PowerControl []struct {
		PowerConsumedWatts num `json:"PowerConsumedWatts"`
	} `json:"PowerControl"`
	PowerSupplies []powerSupply `json:"PowerSupplies"`
	Redundancy    []redundancy  `json:"Redundancy"`
}

// Collect polls one controller. It returns an error when the controller cannot be reached or
// rejects the login; parts that cannot be read are left out or become unknown checks.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.Redfish == nil || strings.TrimSpace(d.Redfish.Username) == "" {
		return nil, errors.New("Redfish-Zugang fehlt (Benutzer und Kennwort)")
	}
	if strings.TrimSpace(d.Host) == "" {
		return nil, errors.New("Host fehlt")
	}
	st := c.state(d)
	cl := st.cl
	root, sys, err := readSystem(ctx, cl)
	if err != nil {
		return nil, err
	}
	firmware := managerFirmware(ctx, cl, root)

	var th thermal
	var pw power
	thOK, pwOK := false, false
	if root.Chassis.ID != "" {
		paths, _ := cl.members(ctx, root.Chassis.ID)
		for _, p := range paths {
			var ch chassis
			if cl.get(ctx, p, &ch) != nil {
				continue
			}
			if ch.Thermal.ID != "" {
				var t thermal
				if cl.get(ctx, ch.Thermal.ID, &t) == nil {
					th.Temperatures = append(th.Temperatures, t.Temperatures...)
					th.Fans = append(th.Fans, t.Fans...)
					th.Redundancy = append(th.Redundancy, t.Redundancy...)
					thOK = true
				}
			}
			if ch.Power.ID != "" {
				var p power
				if cl.get(ctx, ch.Power.ID, &p) == nil {
					pw.PowerControl = append(pw.PowerControl, p.PowerControl...)
					pw.PowerSupplies = append(pw.PowerSupplies, p.PowerSupplies...)
					pw.Redundancy = append(pw.Redundancy, p.Redundancy...)
					pwOK = true
				}
			}
		}
	}
	var checks []model.Check
	if pwOK {
		checks = append(checks, powerCheck(pw))
	}
	if thOK {
		checks = append(checks, fanCheck(th), tempCheck(th))
	}
	checks = append(checks, memoryCheck(sys), cpuCheck(sys))
	checks = append(checks, c.storage(ctx, d.ID, st, sys)...)
	return append([]model.Check{systemCheck(root, sys, firmware, checks)}, checks...), nil
}

// readSystem reads the service root and the first system.
func readSystem(ctx context.Context, cl *client) (serviceRoot, system, error) {
	var root serviceRoot
	if err := cl.get(ctx, "/redfish/v1/", &root); err != nil {
		return root, system{}, err
	}
	if root.Systems.ID == "" {
		return root, system{}, errors.New("Der Controller meldet kein System (Redfish-Dienststamm ohne Systems)")
	}
	paths, err := cl.members(ctx, root.Systems.ID)
	if err != nil {
		return root, system{}, err
	}
	if len(paths) == 0 {
		return root, system{}, errors.New("Der Controller meldet kein System")
	}
	var sys system
	err = cl.get(ctx, paths[0], &sys)
	return root, sys, err
}

func (c *Collector) state(d model.Device) *devState {
	c.mu.Lock()
	defer c.mu.Unlock()
	sig := signature(d.Host, *d.Redfish)
	st := c.devs[d.ID]
	if st == nil || st.sig != sig {
		if st != nil {
			st.cl.http.CloseIdleConnections()
		}
		st = &devState{sig: sig, cl: newClient(d.Host, *d.Redfish)}
		c.devs[d.ID] = st
	}
	return st
}

func managerFirmware(ctx context.Context, cl *client, root serviceRoot) string {
	if root.Managers.ID == "" {
		return ""
	}
	paths, err := cl.members(ctx, root.Managers.ID)
	if err != nil || len(paths) == 0 {
		return ""
	}
	var m struct {
		Model           string `json:"Model"`
		FirmwareVersion string `json:"FirmwareVersion"`
	}
	if cl.get(ctx, paths[0], &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Model + " " + m.FirmwareVersion)
}

// health maps a Redfish status to a check state; present is false for absent components.
func health(s status) (state string, present bool) {
	if s.State == "Absent" {
		return "", false
	}
	switch s.Health {
	case "OK":
		return model.StateOK, true
	case "Warning":
		return model.StateWarn, true
	case "Critical":
		return model.StateCrit, true
	}
	switch s.State {
	case "Enabled", "StandbySpare", "Quiesced", "":
		return model.StateOK, true
	}
	return model.StateUnknown, true
}

func worse(a, b string) string {
	if model.Rank(b) > model.Rank(a) {
		return b
	}
	return a
}

var healthText = map[string]string{"OK": "OK", "Warning": "Warnung", "Critical": "Kritisch"}

func healthWord(s status) string {
	if t, ok := healthText[s.Health]; ok {
		return t
	}
	if s.State != "" {
		return s.State
	}
	return "unbekannt"
}

// systemCheck is "rf:system": model, serial and power state; a powered-off server is
// critical. When the controller's overall health is worse than every component check, it
// decides (a fault in a part not read here, e.g. a voltage or the RAID battery).
func systemCheck(root serviceRoot, sys system, firmware string, parts []model.Check) model.Check {
	ch := model.Check{Key: "rf:system", Name: "Server", Group: groupSystem, State: model.StateOK, Facts: map[string]string{}}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			ch.Facts[k] = v
		}
	}
	set("Hersteller", sys.Manufacturer)
	set("Modell", sys.Model)
	set("Seriennummer", sys.SerialNumber)
	if sys.SKU != sys.SerialNumber {
		set("Service-Tag / SKU", sys.SKU)
	}
	set("BIOS", sys.BiosVersion)
	set("Controller", firmware)
	set("Hostname", sys.HostName)
	set("Redfish", root.Version)
	rollup := cmp.Or(sys.Status.HealthRollup, sys.Status.Health)
	set("Gesamtzustand", healthWord(status{Health: rollup, State: sys.Status.State}))
	switch sys.PowerState {
	case "On":
		ch.Message = "eingeschaltet"
	case "Off":
		ch.State, ch.Message = model.StateCrit, "Server ist ausgeschaltet"
	case "":
		ch.Message = "Stromzustand unbekannt"
	default:
		ch.Message = "Stromzustand " + sys.PowerState
	}
	if title := strings.TrimSpace(sys.Manufacturer + " " + sys.Model); title != "" {
		ch.Message = title + " · " + ch.Message
	}
	worst := model.StateOK
	for _, p := range parts {
		worst = worse(worst, p.State)
	}
	if st, _ := health(status{Health: rollup}); (st == model.StateWarn || st == model.StateCrit) && model.Rank(st) > model.Rank(worst) {
		ch.State = worse(ch.State, st)
		ch.Message += " · Gesamtzustand laut Controller: " + healthText[rollup] + " – Details im Protokoll des Controllers"
	}
	return ch
}

// powerCheck is "rf:power": every present power supply, the redundancy and the consumption.
func powerCheck(p power) model.Check {
	ch := model.Check{Key: "rf:power", Name: "Netzteile", Group: groupPower, State: model.StateOK, Facts: map[string]string{}}
	var bad []string
	n := 0
	for i, ps := range p.PowerSupplies {
		st, present := health(ps.Status)
		if !present {
			continue
		}
		n++
		name := cmp.Or(strings.TrimSpace(ps.Name), "Netzteil "+strconv.Itoa(i+1))
		fact := healthWord(ps.Status)
		if ps.PowerCapacityWatts.ok && ps.PowerCapacityWatts.v > 0 {
			fact += fmt.Sprintf(" · %.0f W", ps.PowerCapacityWatts.v)
		}
		if ps.LineInputVoltage.ok {
			fact += fmt.Sprintf(" · Eingang %.0f V", ps.LineInputVoltage.v)
		}
		if m := strings.TrimSpace(ps.Model); m != "" {
			fact += " · " + m
		}
		ch.Facts[name] = fact
		if st != model.StateOK {
			ch.State = worse(ch.State, st)
			msg := name + " " + strings.ToLower(healthWord(ps.Status))
			if ps.LineInputVoltage.ok && ps.LineInputVoltage.v == 0 {
				msg += " (kein Eingangsstrom – Kabel oder Stromkreis prüfen)"
			}
			bad = append(bad, msg)
		}
	}
	for _, r := range p.Redundancy {
		if st, present := health(r.Status); present && st != model.StateOK {
			ch.State = worse(ch.State, model.StateWarn)
			bad = append(bad, "Redundanz verloren")
			break
		}
	}
	if len(p.Redundancy) > 0 {
		ch.Facts["Redundanz"] = cmp.Or(p.Redundancy[0].Mode, "ja") + " · " + healthWord(p.Redundancy[0].Status)
	}
	for _, pc := range p.PowerControl {
		if pc.PowerConsumedWatts.ok {
			ch.Metrics = []model.Metric{{Name: "power.watts", Value: pc.PowerConsumedWatts.v, Unit: "W"}}
			break
		}
	}
	switch {
	case len(bad) > 0:
		ch.Message = strings.Join(bad, " · ")
	case n == 0:
		ch.Message = "Keine Netzteile gemeldet"
	default:
		ch.Message = fmt.Sprintf("%d %s in Ordnung", n, plural(n, "Netzteil", "Netzteile"))
	}
	if len(ch.Metrics) > 0 {
		ch.Message += fmt.Sprintf(" · Verbrauch %.0f W", ch.Metrics[0].Value)
	}
	return ch
}

// fanCheck is "rf:fans": every present fan (speed as fan.rpm for RPM readings).
func fanCheck(t thermal) model.Check {
	ch := model.Check{Key: "rf:fans", Name: "Lüfter", Group: groupCool, State: model.StateOK}
	var bad []string
	n := 0
	for i, f := range t.Fans {
		st, present := health(f.Status)
		if !present {
			continue
		}
		n++
		name := cmp.Or(strings.TrimSpace(f.Name), strings.TrimSpace(f.FanName), "Lüfter "+strconv.Itoa(i+1))
		if f.Reading.ok && strings.EqualFold(f.ReadingUnits, "RPM") {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "fan.rpm", Instance: name, Value: f.Reading.v, Unit: "rpm"})
		}
		if st != model.StateOK {
			ch.State = worse(ch.State, st)
			bad = append(bad, name+" "+strings.ToLower(healthWord(f.Status)))
		}
	}
	for _, r := range t.Redundancy {
		if st, present := health(r.Status); present && st != model.StateOK {
			ch.State = worse(ch.State, model.StateWarn)
			bad = append(bad, "Lüfter-Redundanz verloren")
			break
		}
	}
	switch {
	case len(bad) > 0:
		ch.Message = strings.Join(bad, " · ")
	case n == 0:
		ch.Message = "Keine Lüfter gemeldet"
	default:
		ch.Message = fmt.Sprintf("%d Lüfter in Ordnung", n)
	}
	return ch
}

// tempCheck is "rf:temps": every temperature sensor with a reading (metric temp), judged by
// the sensor's own thresholds and health.
func tempCheck(t thermal) model.Check {
	ch := model.Check{Key: "rf:temps", Name: "Temperaturen", Group: groupCool, State: model.StateOK, Facts: map[string]string{}}
	var bad []string
	hottest, hotName, inlet := -1000.0, "", ""
	for i, s := range t.Temperatures {
		st, present := health(s.Status)
		if !present || !s.ReadingCelsius.ok {
			continue
		}
		name := cmp.Or(strings.TrimSpace(s.Name), "Sensor "+strconv.Itoa(i+1))
		v := s.ReadingCelsius.v
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "temp", Instance: name, Value: v, Unit: "°C"})
		switch {
		case s.UpperFatal.ok && v >= s.UpperFatal.v, s.UpperCrit.ok && v >= s.UpperCrit.v:
			st = worse(st, model.StateCrit)
		case s.UpperNonCrit.ok && v >= s.UpperNonCrit.v:
			st = worse(st, model.StateWarn)
		}
		if st == model.StateWarn || st == model.StateCrit {
			ch.State = worse(ch.State, st)
			bad = append(bad, fmt.Sprintf("%s %.0f °C", name, v))
		}
		if v > hottest {
			hottest, hotName = v, name
		}
		low := strings.ToLower(name)
		if inlet == "" && (strings.Contains(low, "inlet") || strings.Contains(low, "ambient") || strings.Contains(low, "einlass")) {
			inlet = fmt.Sprintf("%.0f °C", v)
			ch.Facts["Zuluft"] = inlet
		}
	}
	switch {
	case len(ch.Metrics) == 0:
		ch.Message = "Keine Temperatursensoren gemeldet"
	case len(bad) > 0:
		ch.Message = "zu warm: " + strings.Join(bad, ", ")
	default:
		ch.Message = fmt.Sprintf("%d Sensoren in Ordnung · höchste %s %.0f °C", len(ch.Metrics), hotName, hottest)
		if inlet != "" {
			ch.Message = "Zuluft " + inlet + " · " + ch.Message
		}
	}
	return ch
}

// summaryCheck turns a MemorySummary / ProcessorSummary status into a check.
func summaryCheck(key, name string, s status, prefix, hint string) model.Check {
	ch := model.Check{Key: key, Name: name, Group: groupSystem, State: model.StateOK}
	h := cmp.Or(s.HealthRollup, s.Health)
	if h == "" && s.State == "" {
		ch.Message = "Zustand nicht gemeldet"
	} else {
		ch.State, _ = health(status{Health: h, State: s.State})
		ch.Message = "Zustand " + healthWord(status{Health: h, State: s.State})
	}
	if prefix != "" {
		ch.Message = prefix + " · " + ch.Message
	}
	if ch.State == model.StateWarn || ch.State == model.StateCrit {
		ch.Message += " – " + hint
	}
	return ch
}

func memoryCheck(s system) model.Check {
	prefix := ""
	if g := s.MemorySummary.TotalSystemMemoryGiB; g.ok && g.v > 0 {
		prefix = fmt.Sprintf("%.0f GiB", g.v)
	}
	return summaryCheck("rf:memory", "Arbeitsspeicher", s.MemorySummary.Status, prefix, "fehlerhaftes Modul im Protokoll des Controllers nachsehen")
}

func cpuCheck(s system) model.Check {
	prefix := ""
	if n := s.ProcessorSummary.Count; n > 0 {
		prefix = fmt.Sprintf("%d × %s", n, cmp.Or(strings.TrimSpace(s.ProcessorSummary.Model), "CPU"))
	}
	return summaryCheck("rf:cpu", "Prozessoren", s.ProcessorSummary.Status, prefix, "Details im Protokoll des Controllers")
}

type storageRes struct {
	ID                 string `json:"Id"`
	Name               string `json:"Name"`
	Status             status `json:"Status"`
	StorageControllers []struct {
		Name            string `json:"Name"`
		Model           string `json:"Model"`
		FirmwareVersion string `json:"FirmwareVersion"`
		Status          status `json:"Status"`
	} `json:"StorageControllers"`
	Drives  []link `json:"Drives"`
	Volumes link   `json:"Volumes"`
}

type drive struct {
	ID                            string `json:"Id"`
	Name                          string `json:"Name"`
	Model                         string `json:"Model"`
	SerialNumber                  string `json:"SerialNumber"`
	CapacityBytes                 num    `json:"CapacityBytes"`
	MediaType                     string `json:"MediaType"`
	Protocol                      string `json:"Protocol"`
	FailurePredicted              bool   `json:"FailurePredicted"`
	PredictedMediaLifeLeftPercent num    `json:"PredictedMediaLifeLeftPercent"`
	Status                        status `json:"Status"`
}

type volume struct {
	ID            string      `json:"Id"`
	Name          string      `json:"Name"`
	RAIDType      string      `json:"RAIDType"`
	VolumeType    string      `json:"VolumeType"`
	CapacityBytes num         `json:"CapacityBytes"`
	Status        status      `json:"Status"`
	Operations    []operation `json:"Operations"`
}

// operation is a running task of a volume (rebuild, initialization …).
type operation struct {
	OperationName      string `json:"OperationName"`
	PercentageComplete num    `json:"PercentageComplete"`
}

// storage returns the RAID controller, volume and drive checks, cached for storageTTL. A
// system without the standard Storage resource (older HPE firmware) has none.
func (c *Collector) storage(ctx context.Context, devID string, st *devState, sys system) []model.Check {
	now := c.now()
	c.mu.Lock()
	cached, at := st.storage, st.storageAt
	c.mu.Unlock()
	if cached != nil && now.Sub(at) < storageTTL {
		return cached
	}
	out := []model.Check{}
	if sys.Storage.ID != "" {
		paths, err := st.cl.members(ctx, sys.Storage.ID)
		if err != nil {
			out = append(out, model.Check{Key: "rf:storage", Name: "RAID", Group: groupRAID, State: model.StateUnknown,
				Message: "Speichercontroller nicht lesbar: " + err.Error()})
		}
		for _, p := range paths {
			var s storageRes
			if st.cl.get(ctx, p, &s) != nil {
				continue
			}
			var vols []volume
			if s.Volumes.ID != "" {
				vp, _ := st.cl.members(ctx, s.Volumes.ID)
				for _, v := range vp {
					var vol volume
					if st.cl.get(ctx, v, &vol) == nil {
						vols = append(vols, vol)
					}
				}
			}
			var drives []drive
			for i, dl := range s.Drives {
				if i >= maxMembers {
					break
				}
				var dr drive
				if st.cl.get(ctx, dl.ID, &dr) == nil {
					drives = append(drives, dr)
				}
			}
			if len(vols) == 0 && len(drives) == 0 {
				continue // e.g. an onboard SATA controller without disks
			}
			out = append(out, storageChecks(s, vols, drives)...)
		}
	}
	c.mu.Lock()
	if c.devs[devID] == st {
		st.storage, st.storageAt = out, now
	}
	c.mu.Unlock()
	return out
}

// fmtBytes formats drive and volume sizes in decimal units, as they are sold.
func fmtBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for b >= 1000 && i < len(units)-1 {
		b /= 1000
		i++
	}
	if i == 0 || b >= 100 || b == float64(int64(b)) {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", b, units[i]), ".", ",", 1)
}

var opText = map[string]string{"Rebuild": "Wiederaufbau", "Initialize": "Initialisierung", "ConsistencyCheck": "Konsistenzprüfung", "Resync": "Resynchronisierung"}

// storageChecks are "rf:ctrl:<storage>" (controller), "rf:vol:<storage>:<volume>" and
// "rf:drive:<storage>:<drive>".
func storageChecks(s storageRes, vols []volume, drives []drive) []model.Check {
	sid := cmp.Or(s.ID, s.Name)
	ctrl := model.Check{Key: "rf:ctrl:" + sid, Name: "Controller " + cmp.Or(strings.TrimSpace(s.Name), sid), Group: groupRAID, Facts: map[string]string{}}
	ctrl.State, _ = health(s.Status)
	for _, sc := range s.StorageControllers {
		if st, present := health(sc.Status); present {
			ctrl.State = worse(ctrl.State, st)
		}
		if sc.Model != "" {
			ctrl.Facts["Modell"] = sc.Model
			ctrl.Name = "Controller " + sc.Model
		}
		if sc.FirmwareVersion != "" {
			ctrl.Facts["Firmware"] = sc.FirmwareVersion
		}
	}
	ctrl.Facts["Volumes"] = strconv.Itoa(len(vols))
	ctrl.Facts["Platten"] = strconv.Itoa(len(drives))
	ctrl.Message = fmt.Sprintf("%d %s · %d %s", len(vols), plural(len(vols), "Volume", "Volumes"), len(drives), plural(len(drives), "Platte", "Platten"))
	if ctrl.State != model.StateOK {
		ctrl.Message = "Controller " + strings.ToLower(healthWord(s.Status)) + " · " + ctrl.Message
	}
	out := []model.Check{ctrl}

	slices.SortFunc(vols, func(a, b volume) int { return strings.Compare(a.ID, b.ID) })
	for _, v := range vols {
		st, present := health(v.Status)
		if !present {
			continue
		}
		name := cmp.Or(strings.TrimSpace(v.Name), v.ID)
		ch := model.Check{Key: "rf:vol:" + sid + ":" + v.ID, Name: "Volume " + name, Group: groupRAID, State: st, Facts: map[string]string{}}
		if raid := cmp.Or(v.RAIDType, v.VolumeType); raid != "" {
			ch.Name += " (" + raid + ")"
			ch.Facts["RAID"] = raid
		}
		if v.CapacityBytes.ok && v.CapacityBytes.v > 0 {
			ch.Facts["Größe"] = fmtBytes(v.CapacityBytes.v)
		}
		ch.Message = healthWord(v.Status)
		if v.Status.State != "" && v.Status.State != "Enabled" {
			ch.Message += " (" + v.Status.State + ")"
		}
		for _, op := range v.Operations {
			if op.OperationName == "" {
				continue
			}
			txt := cmp.Or(opText[op.OperationName], op.OperationName)
			if op.PercentageComplete.ok {
				txt += fmt.Sprintf(" %.0f %%", op.PercentageComplete.v)
			}
			ch.Message += " · " + txt
			if op.OperationName == "Rebuild" {
				ch.State = worse(ch.State, model.StateWarn)
			}
		}
		if ch.State == model.StateWarn || ch.State == model.StateCrit {
			ch.Message += " – eine Platte ist ausgefallen oder wird ersetzt"
		}
		out = append(out, ch)
	}

	slices.SortFunc(drives, func(a, b drive) int { return strings.Compare(a.ID, b.ID) })
	for _, dr := range drives {
		st, present := health(dr.Status)
		if !present {
			continue
		}
		name := cmp.Or(strings.TrimSpace(dr.Name), dr.ID)
		ch := model.Check{Key: "rf:drive:" + sid + ":" + dr.ID, Name: "Platte " + name, Group: groupDrives, State: st, Facts: map[string]string{}}
		set := func(k, v string) {
			if v = strings.TrimSpace(v); v != "" {
				ch.Facts[k] = v
			}
		}
		set("Modell", dr.Model)
		set("Seriennummer", dr.SerialNumber)
		set("Typ", dr.MediaType+" "+dr.Protocol)
		size := ""
		if dr.CapacityBytes.ok && dr.CapacityBytes.v > 0 {
			size = fmtBytes(dr.CapacityBytes.v)
			set("Größe", size)
		}
		switch {
		case dr.FailurePredicted:
			ch.State = worse(ch.State, model.StateWarn)
			ch.Message = "Ausfall vorhergesagt – Platte tauschen"
		case ch.State == model.StateCrit:
			ch.Message = "Platte ausgefallen"
		default:
			ch.Message = healthWord(dr.Status)
		}
		if l := dr.PredictedMediaLifeLeftPercent; l.ok {
			ch.Metrics = []model.Metric{{Name: "disk.wearout", Instance: name, Value: max(0, 100-l.v), Unit: "%"}}
			set("Restlebensdauer", fmt.Sprintf("%.0f %%", l.v))
		}
		if desc := strings.TrimSpace(strings.TrimSpace(dr.MediaType+" "+size) + " " + dr.Model); desc != "" {
			ch.Message += " · " + desc
		}
		out = append(out, ch)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// TestResult is the answer of a connection test.
type TestResult struct {
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Serial   string `json:"serial"`
	Power    string `json:"power"`
	Firmware string `json:"firmware"`
}

// Test logs in and reads the system.
func Test(ctx context.Context, host string, cfg model.RedfishConfig) (TestResult, error) {
	cl := newClient(host, cfg)
	defer cl.http.CloseIdleConnections()
	root, sys, err := readSystem(ctx, cl)
	if err != nil {
		return TestResult{}, err
	}
	return TestResult{Vendor: cmp.Or(sys.Manufacturer, root.Vendor), Model: sys.Model, Serial: cmp.Or(sys.SKU, sys.SerialNumber),
		Power: sys.PowerState, Firmware: managerFirmware(ctx, cl, root)}, nil
}
