// Package nut polls UPS over a NUT server (Network UPS Tools upsd, RFC 9271), e.g. the
// built-in NUT server of a UniFi UPS: LIST UPS, LIST VAR <ups>, optional USERNAME and
// PASSWORD. Every UPS becomes one check "nut:<ups>".
package nut

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Collector polls NUT devices. It keeps no state between polls.
type Collector struct {
	Timeout time.Duration // per poll; 0 = DefaultTimeout
}

// New returns a collector with the default timeout.
func New() *Collector { return &Collector{Timeout: DefaultTimeout} }

// open connects and logs in.
func open(ctx context.Context, host string, cfg model.NUTConfig, timeout time.Duration) (*client, error) {
	c, err := dial(ctx, host, cfg.Port, timeout)
	if err != nil {
		return nil, err
	}
	if err := c.login(cfg.Username, cfg.Password); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// selectUPS returns the UPS to poll: the configured one (with its description from
// LIST UPS when the server lists it) or every listed UPS. missing is set when the
// configured UPS is not in the list.
func selectUPS(c *client, want string) (sel []upsEntry, all []upsEntry, missing bool, err error) {
	all, err = c.listUPS()
	if err != nil {
		// Without a list, the configured UPS can still be read directly.
		if want != "" && errCode(err) != "" && errCode(err) != "ACCESS-DENIED" {
			return []upsEntry{{name: want}}, nil, false, nil
		}
		return nil, nil, false, listErr(err)
	}
	if want == "" {
		if len(all) == 0 {
			return nil, nil, false, errors.New("NUT-Server meldet keine USV (upsd ohne konfigurierte USV?)")
		}
		return all, all, false, nil
	}
	for _, e := range all {
		if e.name == want {
			return []upsEntry{e}, all, false, nil
		}
	}
	for _, e := range all {
		if strings.EqualFold(e.name, want) {
			return []upsEntry{e}, all, false, nil
		}
	}
	return []upsEntry{{name: want}}, all, true, nil
}

func listErr(err error) error {
	if errCode(err) == "ACCESS-DENIED" {
		return errors.New("NUT-Server: Zugriff verweigert – Benutzername und Passwort prüfen")
	}
	return err
}

func names(es []upsEntry) string {
	n := make([]string, len(es))
	for i, e := range es {
		n[i] = e.name
	}
	return strings.Join(n, ", ")
}

// Collect polls the NUT server at d.Host and returns one check per UPS. The error is set
// only when the server cannot be reached or refuses the login or the listing; a UPS
// without current data becomes an unknown check.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	cfg := model.NUTConfig{}
	if d.NUT != nil {
		cfg = *d.NUT
	}
	cl, err := open(ctx, d.Host, cfg, c.Timeout)
	if err != nil {
		return nil, err
	}
	defer cl.close()

	sel, all, missing, err := selectUPS(cl, strings.TrimSpace(cfg.UPS))
	if err != nil {
		return nil, err
	}
	multi := len(sel) > 1
	checks := make([]model.Check, 0, len(sel))
	for _, u := range sel {
		ck := model.Check{Key: "nut:" + u.name, Name: checkName(u), Group: "USV", Facts: map[string]string{}}
		if missing {
			ck.State = model.StateUnknown
			ck.Message = "USV „" + u.name + "“ ist auf dem NUT-Server nicht bekannt"
			if len(all) > 0 {
				ck.Message += " (vorhanden: " + names(all) + ")"
			}
			checks = append(checks, ck)
			continue
		}
		vars, err := cl.vars(u.name)
		if err != nil {
			code := errCode(err)
			if code == "" {
				return nil, err // connection lost
			}
			if code == "ACCESS-DENIED" {
				return nil, listErr(err)
			}
			ck.State, ck.Message = model.StateUnknown, varsErrText(code)
			checks = append(checks, ck)
			continue
		}
		fill(&ck, u, vars, multi)
		checks = append(checks, ck)
	}
	return checks, nil
}

func checkName(u upsEntry) string {
	if u.desc != "" {
		return u.desc
	}
	return "USV " + u.name
}

// varsErrText is the message of a UPS whose variables could not be read.
func varsErrText(code string) string {
	switch code {
	case "DATA-STALE":
		return "Keine aktuellen Daten – der USV-Treiber meldet veraltete Werte (DATA-STALE)"
	case "DRIVER-NOT-CONNECTED":
		return "USV-Treiber nicht verbunden – USV nicht angeschlossen oder Treiber gestoppt"
	case "UNKNOWN-UPS":
		return "USV auf dem NUT-Server nicht bekannt"
	}
	return "Abfrage fehlgeschlagen: " + codeText(code) + " (" + code + ")"
}

// vars is a UPS's variables with typed accessors.
type vars map[string]string

func (v vars) str(names ...string) string {
	for _, n := range names {
		if s := strings.TrimSpace(v[n]); s != "" {
			return s
		}
	}
	return ""
}

func (v vars) num(name string) (float64, bool) {
	s := strings.TrimSpace(v[name])
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// fill sets state, message, metrics and facts of a UPS check from its variables. With
// multi (several UPS in one device), metrics carry the UPS name as instance so the
// series of the UPS do not mix.
func fill(ck *model.Check, u upsEntry, raw map[string]string, multi bool) {
	v := vars(raw)
	inst := func(i string) string {
		if !multi {
			return i
		}
		if i == "" {
			return u.name
		}
		return u.name + "/" + i
	}
	add := func(name, instance string, val float64, unit string) {
		ck.Metrics = append(ck.Metrics, model.Metric{Name: name, Instance: inst(instance), Value: val, Unit: unit})
	}

	var details []string
	if b, ok := v.num("battery.charge"); ok {
		add("ups.battery", "", b, "%")
		details = append(details, "Ladung "+fmtPct(b))
	}
	if r, ok := v.num("battery.runtime"); ok {
		m := math.Round(r/60*10) / 10
		add("ups.runtime", "", m, "min")
		details = append(details, "Restlaufzeit "+fmtNum(math.Floor(m), 0)+" min")
	}
	load, hasLoad := v.num("ups.load")
	if hasLoad {
		add("ups.load", "", load, "%")
		details = append(details, "Last "+fmtPct(load))
	}
	if x, ok := v.num("input.voltage"); ok {
		add("ups.input", "", x, "V")
	}
	if x, ok := v.num("output.voltage"); ok {
		add("ups.output", "", x, "V")
	}
	if p, ok := v.num("ups.realpower"); ok {
		add("ups.power", "", p, "W")
	} else if n, ok := v.num("ups.realpower.nominal"); ok && hasLoad {
		add("ups.power", "", math.Round(n*load/100*10)/10, "W")
	}
	if x, ok := v.num("battery.voltage"); ok {
		add("ups.battery_voltage", "", x, "V")
	}
	if t, ok := v.num("ups.temperature"); ok {
		add("temp", "USV", t, "°C")
	}
	if t, ok := v.num("battery.temperature"); ok {
		add("temp", "Batterie", t, "°C")
	}

	facts := map[string]string{
		"Hersteller":   v.str("ups.mfr", "device.mfr"),
		"Modell":       v.str("ups.model", "device.model"),
		"Seriennummer": v.str("ups.serial", "device.serial"),
		"Firmware":     v.str("ups.firmware"),
		"Akku-Datum":   v.str("battery.date", "battery.mfr.date"),
		"Treiber":      v.str("driver.name"),
		"Status":       v.str("ups.status"),
	}
	if u.desc != "" {
		facts["USV-Name"] = u.name
	}
	for k, val := range facts {
		if val != "" {
			ck.Facts[k] = val
		}
	}

	status := v.str("ups.status")
	if status == "" {
		ck.State, ck.Message = model.StateUnknown, "Kein Status vom USV-Treiber (ups.status fehlt)"
	} else {
		ck.State, ck.Message = statusText(status, v.str("ups.alarm"))
	}
	if len(details) > 0 {
		ck.Message += " – " + strings.Join(details, ", ")
	}
}

// flagInfo is the meaning of one ups.status flag, in NUT's documented order.
var flagInfo = []struct{ flag, state, text string }{
	{"OL", model.StateOK, "Netzbetrieb"},
	{"OB", model.StateWarn, "Läuft auf Batterie"},
	{"LB", model.StateCrit, "Akku schwach"},
	{"HB", model.StateOK, "Akkuspannung hoch"},
	{"RB", model.StateWarn, "Akku tauschen"},
	{"CHRG", model.StateOK, "Akku lädt"},
	{"DISCHRG", model.StateOK, "Akku entlädt"},
	{"BYPASS", model.StateWarn, "Bypass"},
	{"CAL", model.StateOK, "Kalibrierung läuft"},
	{"OFF", model.StateCrit, "Ausgeschaltet"},
	{"OVER", model.StateCrit, "Überlast"},
	{"TRIM", model.StateOK, "Spannung wird abgesenkt"},
	{"BOOST", model.StateOK, "Spannung wird angehoben"},
	{"FSD", model.StateCrit, "Wird heruntergefahren"},
	{"ALARM", model.StateWarn, "Alarm"},
	{"TEST", model.StateOK, "Selbsttest läuft"},
	{"ECO", model.StateOK, "Eco-Modus"},
	{"WAIT", model.StateUnknown, "Warte auf Daten vom Treiber"},
}

// statusText turns the ups.status flags (e.g. "OL CHRG") into a state and a German
// text ("Netzbetrieb, Akku lädt"). Unknown flags are named in the text but do not change
// the state.
func statusText(status, alarm string) (state, text string) {
	set := map[string]bool{}
	var unknown []string
	for _, f := range strings.Fields(status) {
		u := strings.ToUpper(f)
		known := false
		for _, fi := range flagInfo {
			if fi.flag == u {
				known = true
				break
			}
		}
		if !known && !set[u] {
			unknown = append(unknown, f)
		}
		set[u] = true
	}
	state = model.StateOK
	var parts []string
	for _, fi := range flagInfo {
		if !set[fi.flag] {
			continue
		}
		if fi.flag == "DISCHRG" && set["OB"] {
			continue // implied by "Läuft auf Batterie"
		}
		t := fi.text
		if fi.flag == "ALARM" && alarm != "" {
			t += ": " + alarm
		}
		state = model.Worst(state, fi.state)
		parts = append(parts, t)
	}
	for _, f := range unknown {
		parts = append(parts, "Status "+f)
	}
	if len(parts) == 0 {
		return model.StateUnknown, "Kein Status vom USV-Treiber"
	}
	return state, strings.Join(parts, ", ")
}

// UPSInfo is one UPS found by Test.
type UPSInfo struct {
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Model  string `json:"model"`
	Status string `json:"status"` // German status text, or why no data could be read
}

// TestResult is what Test found on a NUT server.
type TestResult struct {
	Server string    `json:"server"` // server version (VER), else host:port
	UPS    []UPSInfo `json:"ups"`
}

// Test connects to the NUT server, logs in and lists its UPS (only cfg.UPS when set,
// which must exist). The error explains in German what failed.
func Test(ctx context.Context, host string, cfg model.NUTConfig) (TestResult, error) {
	cl, err := open(ctx, host, cfg, DefaultTimeout)
	if err != nil {
		return TestResult{}, err
	}
	defer cl.close()

	res := TestResult{Server: cl.addr}
	if ver, err := cl.raw("VER"); err == nil {
		if ver = strings.TrimSpace(ver); ver != "" {
			if len(ver) > 200 {
				ver = ver[:200]
			}
			res.Server = ver
		}
	} else if errCode(err) == "" {
		return TestResult{}, err
	}

	want := strings.TrimSpace(cfg.UPS)
	sel, all, missing, err := selectUPS(cl, want)
	if err != nil {
		return TestResult{}, err
	}
	if missing {
		msg := "USV „" + want + "“ ist auf dem NUT-Server nicht bekannt"
		if len(all) > 0 {
			msg += " (vorhanden: " + names(all) + ")"
		} else {
			msg += " (der Server meldet keine USV)"
		}
		return TestResult{}, errors.New(msg)
	}
	for _, u := range sel {
		info := UPSInfo{Name: u.name, Desc: u.desc}
		raw, err := cl.vars(u.name)
		switch code := errCode(err); {
		case err == nil:
			v := vars(raw)
			info.Model = v.str("ups.model", "device.model")
			if s := v.str("ups.status"); s != "" {
				_, info.Status = statusText(s, v.str("ups.alarm"))
			} else {
				info.Status = "Kein Status vom USV-Treiber"
			}
		case code == "":
			return TestResult{}, err
		case code == "ACCESS-DENIED":
			return TestResult{}, listErr(err)
		case code == "UNKNOWN-UPS" && want != "":
			return TestResult{}, fmt.Errorf("USV „%s“ ist auf dem NUT-Server nicht bekannt", want)
		default:
			info.Status = varsErrText(code)
		}
		res.UPS = append(res.UPS, info)
	}
	return res, nil
}

// fmtNum formats v with at most prec decimals and a German decimal comma.
func fmtNum(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return strings.Replace(s, ".", ",", 1)
}

func fmtPct(v float64) string { return fmtNum(math.Round(v), 0) + " %" }
