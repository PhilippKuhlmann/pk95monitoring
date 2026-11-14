package core

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"pk95monitoring/internal/model"
)

// metricLabels are the German labels and units of the metrics (keep in sync with
// METRIC_LABELS in web/app.js).
var metricLabels = map[string][2]string{
	"cpu.usage":          {"CPU-Auslastung", "%"},
	"cpu.load1":          {"Systemlast (1 Min.)", ""},
	"mem.usage":          {"Arbeitsspeicher", "%"},
	"mem.used":           {"Arbeitsspeicher belegt", "B"},
	"mem.total":          {"Arbeitsspeicher gesamt", "B"},
	"swap.usage":         {"Auslagerung", "%"},
	"disk.usage":         {"Festplattenbelegung", "%"},
	"disk.used":          {"Festplatte belegt", "B"},
	"disk.total":         {"Festplatte gesamt", "B"},
	"net.rx":             {"Netzwerk empfangen", "B/s"},
	"net.tx":             {"Netzwerk gesendet", "B/s"},
	"uptime":             {"Laufzeit", "s"},
	"procs":              {"Prozesse", ""},
	"services.failed":    {"Ausgefallene Dienste", ""},
	"updates.pending":    {"Ausstehende Updates", ""},
	"updates.security":   {"Sicherheitsupdates", ""},
	"updates.age":        {"Letzte Update-Installation (Tage)", "d"},
	"updates.failed":     {"Fehlgeschlagene Updates", ""},
	"backup.age":         {"Alter der letzten Sicherung", "h"},
	"backup.size":        {"Größe der letzten Sicherung", "B"},
	"job.age":            {"Letzter Lauf vor", "h"},
	"eventlog.errors":    {"Fehler im Ereignisprotokoll (24 Std.)", ""},
	"events.count":       {"Ereignisse", ""},
	"guest.cpu":          {"Gast-CPU", "%"},
	"guest.mem":          {"Gast-Arbeitsspeicher", "%"},
	"guest.mem_used":     {"Gast-Arbeitsspeicher belegt", "B"},
	"guest.netin":        {"Gast-Netzwerk empfangen", "B/s"},
	"guest.netout":       {"Gast-Netzwerk gesendet", "B/s"},
	"guest.diskread":     {"Gast-Datenträger lesen", "B/s"},
	"guest.diskwrite":    {"Gast-Datenträger schreiben", "B/s"},
	"storage.usage":      {"Speicherbelegung", "%"},
	"storage.used":       {"Speicher belegt", "B"},
	"storage.total":      {"Speicher gesamt", "B"},
	"ping.rtt":           {"Ping-Laufzeit", "ms"},
	"ping.loss":          {"Paketverlust", "%"},
	"http.time":          {"Antwortzeit", "ms"},
	"http.status":        {"HTTP-Status", ""},
	"cert.days":          {"Zertifikat gültig (Tage)", "d"},
	"domain.days":        {"Domain registriert (Tage)", "d"},
	"tcp.time":           {"TCP-Verbindungszeit", "ms"},
	"dns.time":           {"DNS-Antwortzeit", "ms"},
	"if.rx":              {"Port empfangen", "B/s"},
	"if.tx":              {"Port gesendet", "B/s"},
	"if.errors":          {"Port-Fehler", "1/s"},
	"ups.battery":        {"Akku", "%"},
	"ups.runtime":        {"Restlaufzeit", "min"},
	"ups.load":           {"USV-Last", "%"},
	"ups.input":          {"Eingangsspannung", "V"},
	"ups.output":         {"Ausgangsspannung", "V"},
	"temp":               {"Temperatur", "°C"},
	"printer.toner":      {"Toner", "%"},
	"printer.pages":      {"Gedruckte Seiten", ""},
	"custom":             {"Eigener Wert", ""},
	"cpu.iowait":         {"IO-Wartezeit", "%"},
	"disk.temp":          {"Plattentemperatur", "°C"},
	"disk.wearout":       {"Verschleiß", "%"},
	"disk.spare":         {"Reserve", "%"},
	"disk.reallocated":   {"Neu zugewiesene Sektoren", ""},
	"disk.pending":       {"Ausstehende Sektoren", ""},
	"disk.uncorrectable": {"Nicht korrigierbare Fehler", ""},
	"disk.media_errors":  {"Medienfehler", ""},
	"disk.crc_errors":    {"CRC-Fehler", ""},
	"zfs.usage":          {"ZFS-Belegung", "%"},
	"zfs.frag":           {"ZFS-Fragmentierung", "%"},
	"zfs.errors":         {"ZFS-Fehler", ""},
	"zfs.scrub_age":      {"Letzter Scrub vor (Tagen)", "d"},
	"repl.age":           {"Letzte Replikation vor (Min.)", "min"},
}

// MetricLabel returns the German label of a metric name (the name itself when unknown).
func MetricLabel(name string) string {
	if l, ok := metricLabels[name]; ok {
		return l[0]
	}
	return name
}

// instanceLabel joins a metric label and its instance ("Festplattenbelegung /"). An instance
// that already names the label stands alone: "Magenta Toner", not "Toner Magenta Toner".
func instanceLabel(label, instance string) string {
	switch {
	case instance == "":
		return label
	case strings.Contains(strings.ToLower(instance), strings.ToLower(label)):
		return instance
	}
	return label + " " + instance
}

// redundantInstance reports whether the check name already names every part of a
// "node/device" instance ("Platte nvme1n1 (pve1)" for "pve1/nvme1n1").
func redundantInstance(name, inst string) bool {
	parts := strings.Split(inst, "/")
	if len(parts) < 2 {
		return false
	}
	low := strings.ToLower(name)
	for _, p := range parts {
		if p == "" || !strings.Contains(low, strings.ToLower(p)) {
			return false
		}
	}
	return true
}

func metricUnit(m model.Metric) string {
	if m.Unit != "" {
		return m.Unit
	}
	return metricLabels[m.Name][1]
}

// findThreshold picks the threshold of a metric: device with instance > device by name >
// global with instance > global by name > model.DefaultThresholds. A Disabled hit means
// "no threshold".
func findThreshold(m model.Metric, lists ...[]model.Threshold) (model.Threshold, bool) {
	for _, list := range lists {
		for _, specific := range []bool{true, false} {
			for _, t := range list {
				if t.Specific() == specific && t.Matches(m) {
					return t, !t.Disabled
				}
			}
		}
	}
	return model.Threshold{}, false
}

// judge applies a threshold to a value.
func judge(t model.Threshold, v float64) (state string, limit float64) {
	bad := func(l *float64) bool {
		if l == nil {
			return false
		}
		if t.Below {
			return v <= *l
		}
		return v >= *l
	}
	switch {
	case bad(t.Crit):
		return model.StateCrit, *t.Crit
	case bad(t.Warn):
		return model.StateWarn, *t.Warn
	}
	return model.StateOK, 0
}

// thrResult is the threshold verdict of one check (before hysteresis).
type thrResult struct {
	state   string
	count   int    // consecutive bad values needed (of the worst metric)
	reason  string // e.g. "Festplattenbelegung / 91 % ≥ 85 %" (all metrics at the worst state)
	value   string // value of the first worst metric, e.g. "91 %"
	limit   string // its limit, e.g. "≥ 85 %"
	metrics int    // number of metrics that had a threshold
}

// thresholdsOf judges all metrics of a check.
func thresholdsOf(ch model.Check, lists ...[]model.Threshold) thrResult {
	r := thrResult{state: model.StateOK, count: 1}
	var reasons []string
	for _, m := range ch.Metrics {
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			continue
		}
		t, ok := findThreshold(m, lists...)
		if !ok {
			continue
		}
		r.metrics++
		st, limit := judge(t, m.Value)
		if st == model.StateOK {
			continue
		}
		unit := metricUnit(m)
		op := "≥"
		if t.Below {
			op = "≤"
		}
		inst := m.Instance
		if redundantInstance(ch.Name, inst) {
			inst = "" // "Platte nvme1n1 (pve1)": not "Medienfehler pve1/nvme1n1"
		}
		label := instanceLabel(MetricLabel(m.Name), inst)
		text := label + " " + fmtValue(m.Value, unit) + " " + op + " " + fmtLimit(limit, unit)
		if model.Rank(st) > model.Rank(r.state) {
			r.state, r.count, reasons = st, max(t.Count, 1), nil
			r.value, r.limit = fmtValue(m.Value, unit), op+" "+fmtLimit(limit, unit)
		}
		if st == r.state {
			reasons = append(reasons, text)
		}
	}
	r.reason = strings.Join(reasons, ", ")
	return r
}

// evaluate turns a fresh check into its effective status: thresholds with hysteresis
// (Count consecutive bad values, back to ok immediately), then worst(native, thresholds),
// then the check mode (a quiet check shows ok and keeps the bad state in Muted).
func (c *Core) evaluate(d model.Device, rt *devState, prev map[string]model.CheckStatus, ch model.Check, now int64, global []model.Threshold) model.CheckStatus {
	native := ch.State
	if model.Rank(native) == 1 || native == "" {
		native = model.StateUnknown
	}
	tr := thresholdsOf(ch, d.Thresholds, global, model.DefaultThresholds())
	p, had := prev[ch.Key]
	prevThr := rt.thr[ch.Key]
	if prevThr == "" {
		prevThr = model.StateOK
	}
	thr, pending := tr.state, 0
	if tr.state != model.StateOK && model.Rank(tr.state) > model.Rank(prevThr) {
		pending = p.Pending + 1
		if pending >= tr.count {
			pending = 0
		} else {
			thr = prevThr
		}
	}
	if thr == model.StateOK {
		delete(rt.thr, ch.Key)
	} else {
		rt.thr[ch.Key] = thr
	}

	cs := model.CheckStatus{Check: ch, Native: native, Pending: pending}
	cs.State = model.Worst(native, thr)
	switch {
	case thr != model.StateOK && model.Rank(thr) >= model.Rank(native):
		if thr == tr.state {
			cs.Reason = tr.reason
			rt.detail[ch.Key] = [2]string{tr.value, tr.limit}
		} else { // hysteresis keeps the previous verdict while the value got worse
			cs.Reason = p.Reason
		}
	case native == model.StateWarn || native == model.StateCrit:
		cs.Reason = ch.Message
		delete(rt.detail, ch.Key)
	default:
		delete(rt.detail, ch.Key)
	}
	// Quiet checks keep the verdict (and the hysteresis above) but show ok.
	cs = applyMode(cs, modeOf(d, rt, ch.Key))
	cs.Since = now
	if had && p.State == cs.State {
		cs.Since = p.Since
	}
	return cs
}

// ---- number formatting (German) ----

func deNum(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if decimals > 0 {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.Replace(s, ".", ",", 1)
}

// fmtNum rounds like the UI: no decimals from 10, one below, two below 1.
func fmtNum(v float64) string {
	switch a := math.Abs(v); {
	case a >= 10 || v == math.Trunc(v):
		return deNum(math.Round(v), 0)
	case a >= 1:
		return deNum(v, 1)
	}
	return deNum(v, 2)
}

func humanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(b) >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || math.Abs(b) >= 100 {
		return deNum(math.Round(b), 0) + " " + units[i]
	}
	return deNum(math.Round(b*10)/10, 1) + " " + units[i]
}

func withUnit(num, unit string) string {
	switch unit {
	case "":
		return num
	case "d":
		return num + " Tage"
	}
	return num + " " + unit
}

// fmtValue formats a measured value with its unit, e.g. "91 %", "1,2 GB", "350 ms".
func fmtValue(v float64, unit string) string {
	switch unit {
	case "B":
		return humanBytes(v)
	case "B/s":
		return humanBytes(v) + "/s"
	}
	return withUnit(fmtNum(v), unit)
}

// fmtLimit formats a threshold as entered (no rounding).
func fmtLimit(v float64, unit string) string {
	switch unit {
	case "B":
		return humanBytes(v)
	case "B/s":
		return humanBytes(v) + "/s"
	}
	return withUnit(strings.Replace(strconv.FormatFloat(v, 'f', -1, 64), ".", ",", 1), unit)
}

// ---- threshold validation ----

var metricRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

// validateThresholds checks a threshold list; errors are keyed "thresholds.N.field".
func validateThresholds(list []model.Threshold, errs map[string]string) {
	seen := map[string]int{}
	for i := range list {
		t := &list[i]
		key := func(f string) string { return fmt.Sprintf("thresholds.%d.%s", i, f) }
		t.Metric = strings.TrimSpace(t.Metric)
		name, inst, hasInst := strings.Cut(t.Metric, ":")
		name, inst = strings.TrimSpace(name), strings.TrimSpace(inst)
		switch {
		case name == "":
			errs[key("metric")] = "Messwert auswählen."
		case !metricRe.MatchString(name):
			errs[key("metric")] = "Ungültiger Messwert."
		case hasInst && inst == "":
			errs[key("instance")] = "Instanz angeben oder leer lassen."
		}
		if hasInst {
			t.Metric = name + ":" + inst
		} else {
			t.Metric = name
		}
		if j, dup := seen[t.Metric]; dup && t.Metric != "" {
			errs[key("metric")] = fmt.Sprintf("Doppelt (wie Zeile %d).", j+1)
		}
		seen[t.Metric] = i
		finite := func(p *float64) bool { return p == nil || !(math.IsNaN(*p) || math.IsInf(*p, 0)) }
		if !finite(t.Warn) {
			errs[key("warn")] = "Zahl angeben."
		}
		if !finite(t.Crit) {
			errs[key("crit")] = "Zahl angeben."
		}
		if !t.Disabled && t.Warn == nil && t.Crit == nil {
			errs[key("warn")] = "Warnung oder Kritisch angeben."
		}
		if t.Warn != nil && t.Crit != nil {
			if !t.Below && *t.Crit < *t.Warn {
				errs[key("crit")] = "Kritisch muss mindestens so hoch sein wie Warnung."
			}
			if t.Below && *t.Crit > *t.Warn {
				errs[key("crit")] = "Unterhalb: Kritisch muss höchstens so hoch sein wie Warnung."
			}
		}
		if t.Count < 0 || t.Count > 100 {
			errs[key("count")] = "Anzahl zwischen 1 und 100."
		}
	}
}
