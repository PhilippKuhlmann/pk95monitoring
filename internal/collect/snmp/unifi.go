package snmp

import (
	"regexp"
	"strconv"
	"strings"

	"pk95monitoring/internal/model"
)

// Broadcom FASTPATH (UniFi switches, EdgeSwitch). The tree lives below 4413.1.1; UniFi
// firmware 4.0.14 served it below 41112.1.1 instead, so both bases are tried.
const (
	fpBase4413  = "1.3.6.1.4.1.4413.1.1"
	fpBase41112 = "1.3.6.1.4.1.41112.1.1"

	// suffixes below the base
	fpMachineType = ".1.1.1.2.0"  // agentInventoryMachineType, e.g. "US-24-250W"
	fpSerial      = ".1.1.1.4.0"  // agentInventorySerialNumber
	fpSoftware    = ".1.1.1.13.0" // agentInventorySoftwareVersion, e.g. "5.43.18.12487"
	fpMemFree     = ".1.1.4.1.0"  // agentSwitchCpuProcessMemFree, KB
	fpMemTotal    = ".1.1.4.2.0"  // agentSwitchCpuProcessMemAvailable, KB (the total)
	fpCPUString   = ".1.1.4.9.0"  // agentSwitchCpuProcessTotalUtilization, "5 Secs ( 40.8140%) 60 Secs …"
	fpCPU60       = ".1.1.4.12.0" // integer percent over 60 s (not on every firmware)
	fpTempMin     = ".43.1.1.0"   // boxServicesNormalTempRangeMin, °C
	fpTempMax     = ".43.1.2.0"   // boxServicesNormalTempRangeMax, °C
	fpFanEntry    = ".43.1.6.1"   // boxServicesFansEntry, index unit.fan
	fpPSUEntry    = ".43.1.7.1"   // boxServicesPowSuppliesEntry, index unit.psu
	fpTempEntry   = ".43.1.8.1"   // boxServicesTempSensorsEntry, index unit.sensor
	fpTempUnit    = ".43.1.15.1"  // boxServicesTempUnitEntry, index unit

	fpFanState, fpFanSpeed       = 3, 4
	fpPSUState                   = 3
	fpTempValue                  = 5
	fpTempUnitState, fpTempUnitT = 2, 3

	// UCD-SNMP-MIB memory (KB), UniFi switches on net-snmp
	ucdMemTotal  = "1.3.6.1.4.1.2021.4.5.0"
	ucdMemAvail  = "1.3.6.1.4.1.2021.4.6.0"
	ucdMemBuffer = "1.3.6.1.4.1.2021.4.14.0"
	ucdMemCached = "1.3.6.1.4.1.2021.4.15.0"

	// ENTITY-MIB entPhysicalEntry
	oidEntPhysicalEntry                        = "1.3.6.1.2.1.47.1.1.1.1"
	entDescr, entClass, entSerial, entModel    = 2, 5, 11, 13
	entClassChassis                            = 3
	sysObjectIDUniFiUS8, sysObjectIDNetSNMP    = "1.3.6.1.4.1.41112.1", "1.3.6.1.4.1.8072.3.2.10"
	sysObjectIDFastpath, sysObjectIDFastpathEx = "1.3.6.1.4.1.4413", "1.3.6.1.4.1.4413."
)

// uniFiDescr matches the sysDescr of UniFi switches: "US-24-250W, 5.43.18.12487, Linux 3.6.5",
// "USW-Flex-XG, 6.2.14.13855", "UBNT US48PRO - 48 GE POE + 4 XE, …", "USW GigaBit Ethernet Switch, …".
var uniFiDescr = regexp.MustCompile(`^(?:(?:US|USW)[ -]|UBNT US|USW GigaBit|USW-)`)

// isUniFi detects a UniFi switch by sysDescr and sysObjectID; a FASTPATH device
// (sysObjectID 4413) is asked for its machine type. ok is false when that request failed.
func isUniFi(s Session, si sysInfo) (uni, ok bool) {
	descr, oid := strings.TrimSpace(si.descr), trimDot(si.objectID)
	switch {
	case uniFiDescr.MatchString(descr):
		return true, true
	case oid == sysObjectIDUniFiUS8: // US-8 (Vitesse); APs use other OIDs below 41112
		return true, true
	case oid == sysObjectIDNetSNMP && strings.HasPrefix(descr, "Linux UBNT"):
		return true, true
	case oid == sysObjectIDFastpath || strings.HasPrefix(oid, sysObjectIDFastpathEx):
		v, err := get(s, fpBase4413+fpMachineType)
		if err != nil {
			return false, false
		}
		return uniFiMachine(v.str(fpBase4413 + fpMachineType)), true
	}
	return false, true
}

// uniFiMachine reports whether a FASTPATH machine type is a UniFi switch or an
// EdgeSwitch (same MIBs).
func uniFiMachine(mt string) bool {
	for _, p := range []string{"US", "UBNT", "EdgeSwitch", "EdgePoint"} {
		if strings.HasPrefix(mt, p) {
			return true
		}
	}
	return false
}

// uniFiDescrModel extracts model and firmware from the sysDescr of a UniFi switch or
// EdgeSwitch ("US-24-250W, 5.43.18.12487, Linux 3.6.5" → "US-24-250W", "5.43.18.12487").
var uniFiDescrModel = regexp.MustCompile(`^(?:UBNT\s+)?((?:US|EdgeSwitch|EdgePoint)[^,]*?)(?:,\s*|\s+)(?:firmware\s+)?v?(\d+(?:\.\d+){2,})(?:[,\s]|$)`)

func parseUniFiDescr(descr string) (hw, version string) {
	m := uniFiDescrModel.FindStringSubmatch(strings.TrimSpace(descr))
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(m[1]), m[2]
}

var (
	fpCPUWindow = regexp.MustCompile(`(\d+)\s*Secs?\s*\(\s*(\d+(?:[.,]\d+)?)\s*%\s*\)`)
	fpCPUAny    = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*%`)
)

// parseFastpathCPU reads agentSwitchCpuProcessTotalUtilization, e.g.
// "    5 Secs ( 40.8140%)   60 Secs ( 18.1657%)  300 Secs ( 15.1503%)". It prefers the
// 60-second value, then the first window; window is 0 when no window was named.
func parseFastpathCPU(s string) (pct float64, window int, ok bool) {
	ms := fpCPUWindow.FindAllStringSubmatch(s, -1)
	pick := -1
	for i, m := range ms {
		if m[1] == "60" {
			pick = i
			break
		}
		if pick < 0 {
			pick = i
		}
	}
	if pick >= 0 {
		v, ok := parseNum(ms[pick][2])
		w, _ := strconv.Atoi(ms[pick][1])
		return v, w, ok && v >= 0 && v <= 100
	}
	if m := fpCPUAny.FindStringSubmatch(s); m != nil {
		v, ok := parseNum(m[1])
		return v, 0, ok && v >= 0 && v <= 100
	}
	return 0, 0, false
}

var firstNumber = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// parseLooseNum reads a number from an integer PDU or the first number in a string
// (boxServicesFanSpeed is an OCTET STRING in some MIB versions, Integer32 in others).
func parseLooseNum(v vals, oid string) (float64, bool) {
	if f, ok := v.num(oid); ok {
		return f, true
	}
	if m := firstNumber.FindString(v.str(oid)); m != "" {
		return parseNum(m)
	}
	return 0, false
}

// entityInfo is the chassis row of ENTITY-MIB.
type entityInfo struct{ model, serial string }

// unifi reads the FASTPATH vendor tree of a UniFi switch: inventory facts (Modell,
// Firmware, Seriennummer), CPU and memory (replacing the HOST-RESOURCES-MIB values),
// temperatures, fans and power supplies. Switches without it (UniFi on net-snmp) keep
// HOST-RESOURCES-MIB and get memory from UCD-SNMP-MIB.
func (p *poll) unifi() {
	base, v, err := p.fastpath()
	if err != nil {
		p.optional("UniFi", err)
		if isTimeout(err) {
			return
		}
	}

	mdl, serial, fw := v.str(base+fpMachineType), v.str(base+fpSerial), v.str(base+fpSoftware)
	if base == "" {
		mdl, serial, fw = "", "", ""
	}
	hw, ver := parseUniFiDescr(p.sys.descr)
	if mdl == "" {
		mdl = hw
	}
	if fw == "" {
		fw = ver
	}
	if mdl == "" || serial == "" {
		ent := p.entity()
		if mdl == "" {
			mdl = ent.model
		}
		if serial == "" {
			serial = ent.serial
		}
	}
	p.facts["Modell"], p.facts["Firmware"], p.facts["Seriennummer"] = mdl, fw, serial

	if base != "" {
		if pct, win, ok := parseFastpathCPU(v.str(base + fpCPUString)); ok {
			msg := "Auslastung " + fmtPct(pct)
			if win > 0 {
				msg += " (Mittel über " + strconv.Itoa(win) + " s)"
			}
			p.replace(cpuCheck(pct, msg))
		} else if pct, ok := v.num(base + fpCPU60); ok && pct >= 0 && pct <= 100 {
			p.replace(cpuCheck(pct, "Auslastung "+fmtPct(pct)+" (Mittel über 60 s)"))
		}
		free, okF := v.num(base + fpMemFree)
		total, okT := v.num(base + fpMemTotal)
		if okF && okT && total > 0 && free >= 0 && free <= total {
			p.replace(memCheck((total-free)*1024, total*1024))
		}
	}
	if !p.has("snmp:mem") {
		total, okT := v.num(ucdMemTotal)
		avail, okA := v.num(ucdMemAvail)
		if okT && okA && total > 0 && avail >= 0 && avail <= total {
			buf, _ := v.num(ucdMemBuffer)
			cached, _ := v.num(ucdMemCached)
			used := total - avail
			if used-buf-cached > 0 {
				used -= buf + cached
			}
			p.add(memCheck(used*1024, total*1024))
		}
	}
	if base != "" {
		p.boxServices(base, v)
	}
}

func cpuCheck(pct float64, msg string) model.Check {
	return model.Check{Key: "snmp:cpu", Name: "CPU", Group: "System", State: model.StateOK, Message: msg,
		Metrics: []model.Metric{{Name: "cpu.usage", Value: pct, Unit: "%"}}}
}

func memCheck(used, total float64) model.Check {
	pct := used / total * 100
	return model.Check{Key: "snmp:mem", Name: "Arbeitsspeicher", Group: "System", State: model.StateOK,
		Message: fmtBytes(used) + " von " + fmtBytes(total) + " belegt (" + fmtPct(pct) + ")",
		Metrics: []model.Metric{
			{Name: "mem.usage", Value: pct, Unit: "%"},
			{Name: "mem.used", Value: used, Unit: "B"},
			{Name: "mem.total", Value: total, Unit: "B"},
		}}
}

// fpOIDs are the scalars read below a FASTPATH base in every poll.
var fpOIDs = []string{fpMachineType, fpSerial, fpSoftware, fpMemFree, fpMemTotal, fpCPUString, fpCPU60,
	fpTempMin, fpTempMax}

// fastpath reads the FASTPATH scalars (and the UCD memory) in one GET. The base (4413.1.1,
// the 41112.1.1 of firmware 4.0.14, or none) is detected once and cached for staticTTL.
func (p *poll) fastpath() (string, vals, error) {
	sc := p.staticData()
	bases := []string{fpBase4413, fpBase41112}
	if fresh(sc.fpAt, p.now) {
		bases = []string{sc.fpBase}
	}
	var all vals
	chosen := ""
	for i, base := range bases {
		var req []string
		if i == 0 {
			req = append(req, ucdMemTotal, ucdMemAvail, ucdMemBuffer, ucdMemCached)
		}
		if base != "" {
			for _, o := range fpOIDs {
				req = append(req, base+o)
			}
		}
		v, err := get(p.s, req...)
		if err != nil {
			return "", vals{}, err
		}
		if all == nil {
			all = v
		} else {
			for k, pdu := range v {
				all[k] = pdu
			}
		}
		if base != "" && len(v) > 0 && fpAnswered(v, base) {
			chosen = base
			break
		}
	}
	p.setStatic(func(c *staticCache) { c.fpBase, c.fpAt = chosen, p.now })
	return chosen, all, nil
}

func fpAnswered(v vals, base string) bool {
	for _, o := range fpOIDs {
		if _, ok := v[base+o]; ok {
			return true
		}
	}
	return false
}

// entity returns model and serial number of the chassis row of ENTITY-MIB (cached).
func (p *poll) entity() entityInfo {
	sc := p.staticData()
	if fresh(sc.entAt, p.now) {
		return sc.ent
	}
	t, err := walkTable(p.s, oidEntPhysicalEntry, entClass, entDescr, entSerial, entModel)
	if err != nil {
		p.optional("ENTITY-MIB", err)
		return sc.ent
	}
	var ent entityInfo
	for _, r := range t.rows {
		if c, _ := t.int(entClass, r); c != entClassChassis {
			continue
		}
		ent.model = t.str(entModel, r)
		if ent.model == "" {
			ent.model = t.str(entDescr, r)
		}
		ent.serial = strings.TrimSpace(strings.TrimPrefix(t.str(entSerial, r), "System MAC:"))
		break
	}
	p.setStatic(func(c *staticCache) { c.ent, c.entAt = ent, p.now })
	return ent
}

// unitName prefixes a name with the stack unit when there are several units.
func unitName(unit string, multi bool, name string) string {
	if multi {
		return "Einheit " + unit + " " + name
	}
	return name
}

// units returns the distinct first sub-identifiers (unit numbers) of table rows.
func units(rows []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rows {
		u, _, _ := strings.Cut(r, ".")
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// boxServices reads temperatures (snmp:temp:<name>), fans (snmp:fan:<name>) and power
// supplies (snmp:psu:<name>) of the FASTPATH boxServices group. v holds the scalars.
func (p *poll) boxServices(base string, v vals) {
	tmin, hasMin := v.num(base + fpTempMin)
	tmax, hasMax := v.num(base + fpTempMax)
	if hasMin && hasMax && tmin >= tmax {
		hasMin, hasMax = false, false
	}

	temps, err := walkTable(p.s, base+fpTempEntry, fpTempValue)
	if err != nil {
		p.optional("Temperatur", err)
	}
	unitT, err := walkTable(p.s, base+fpTempUnit, fpTempUnitState, fpTempUnitT)
	if err != nil {
		p.optional("Temperatur", err)
	}
	allUnits := units(append(append([]string{}, temps.rows...), unitT.rows...))
	multi := len(allUnits) > 1
	type sensor struct {
		ck model.Check
		t  float64
	}
	byUnit := map[string][]*sensor{}
	n := map[string]int{}
	for _, r := range temps.rows {
		t, ok := temps.num(fpTempValue, r)
		if !ok || t < -60 || t > 200 {
			continue
		}
		u, _, _ := strings.Cut(r, ".")
		n[u]++
		name := unitName(u, multi, "Sensor "+strconv.Itoa(n[u]))
		ck := tempCheck(name, t, tmin, hasMin, tmax, hasMax)
		byUnit[u] = append(byUnit[u], &sensor{ck: ck, t: t})
	}
	for _, u := range unitT.rows {
		state, _ := unitT.int(fpTempUnitState, u)
		if state == 5 { // notpresent
			continue
		}
		st, msg := tempUnitState(state)
		if len(byUnit[u]) == 0 { // e.g. fanless US-8: only the unit temperature
			t, ok := unitT.num(fpTempUnitT, u)
			if !ok || t < -60 || t > 200 {
				continue
			}
			name := "System"
			if multi {
				name = "Einheit " + u
			}
			ck := tempCheck(name, t, tmin, hasMin, tmax, hasMax)
			byUnit[u] = append(byUnit[u], &sensor{ck: ck, t: t})
		}
		if st == model.StateOK {
			continue
		}
		hot := byUnit[u][0]
		for _, s := range byUnit[u] {
			if s.t > hot.t {
				hot = s
			}
		}
		hot.ck.State = model.Worst(hot.ck.State, st)
		hot.ck.Message += ", " + msg
	}
	for _, u := range allUnits {
		for _, s := range byUnit[u] {
			p.add(s.ck)
		}
	}

	p.fans(base)
	p.psus(base)
}

func tempCheck(name string, t, tmin float64, hasMin bool, tmax float64, hasMax bool) model.Check {
	ck := model.Check{Key: "snmp:temp:" + name, Name: "Temperatur " + name, Group: "Sensoren",
		State: model.StateOK, Message: fmtNum(t, 1) + " °C",
		Metrics: []model.Metric{{Name: "temp", Instance: name, Value: t, Unit: "°C"}}}
	switch {
	case hasMax && t > tmax:
		ck.State = model.StateWarn
		ck.Message += " (über dem Normalbereich bis " + fmtNum(tmax, 0) + " °C)"
	case hasMin && t < tmin:
		ck.State = model.StateWarn
		ck.Message += " (unter dem Normalbereich ab " + fmtNum(tmin, 0) + " °C)"
	}
	return ck
}

// tempUnitState maps boxServicesTempUnitState: low(0) normal(1) warning(2) critical(3)
// shutdown(4) notpresent(5) notoperational(6).
func tempUnitState(s int) (string, string) {
	switch s {
	case 2:
		return model.StateWarn, "Temperaturwarnung des Geräts"
	case 3:
		return model.StateCrit, "Temperatur kritisch"
	case 4:
		return model.StateCrit, "Übertemperatur, Abschaltung"
	case 6:
		return model.StateUnknown, "Temperatursensor nicht betriebsbereit"
	}
	return model.StateOK, ""
}

// fans reads boxServicesFansTable: ItemState notpresent(1) operational(2) failed(3)
// powering(4) nopower(5) notpowering(6) incompatible(7), FanSpeed in rpm.
func (p *poll) fans(base string) {
	t, err := walkTable(p.s, base+fpFanEntry, fpFanState, fpFanSpeed)
	if err != nil {
		p.optional("Lüfter", err)
		return
	}
	multi := len(units(t.rows)) > 1
	n := map[string]int{}
	for _, r := range t.rows {
		state, _ := t.int(fpFanState, r)
		if state == 1 {
			continue
		}
		u, _, _ := strings.Cut(r, ".")
		n[u]++
		name := unitName(u, multi, "Lüfter "+strconv.Itoa(n[u]))
		ck := model.Check{Key: "snmp:fan:" + name, Name: name, Group: "Sensoren", State: model.StateOK}
		rpm, hasRPM := parseLooseNum(vals{"s": t.cols[fpFanSpeed][r]}, "s")
		switch state {
		case 2:
			ck.Message = "In Betrieb"
		case 3, 5, 6:
			ck.State, ck.Message = model.StateCrit, "Lüfter ausgefallen"
		case 4:
			ck.Message = "Läuft an"
		case 7:
			ck.State, ck.Message = model.StateWarn, "Lüfter inkompatibel"
		default:
			ck.State, ck.Message = model.StateUnknown, "Status "+strconv.Itoa(state)
		}
		if hasRPM && rpm >= 0 {
			ck.Metrics = []model.Metric{{Name: "fan.rpm", Instance: name, Value: rpm, Unit: "rpm"}}
			if state == 2 {
				ck.Message = fmtNum(rpm, 0) + " U/min"
			}
		}
		p.add(ck)
	}
}

// psus reads boxServicesPowSuppliesTable (states as for fans).
func (p *poll) psus(base string) {
	t, err := walkTable(p.s, base+fpPSUEntry, fpPSUState)
	if err != nil {
		p.optional("Netzteile", err)
		return
	}
	multi := len(units(t.rows)) > 1
	n := map[string]int{}
	for _, r := range t.rows {
		state, _ := t.int(fpPSUState, r)
		if state == 1 {
			continue
		}
		u, _, _ := strings.Cut(r, ".")
		n[u]++
		name := unitName(u, multi, "Netzteil "+strconv.Itoa(n[u]))
		ck := model.Check{Key: "snmp:psu:" + name, Name: name, Group: "Sensoren", State: model.StateOK}
		switch state {
		case 2:
			ck.Message = "In Betrieb"
		case 3:
			ck.State, ck.Message = model.StateCrit, "Netzteil ausgefallen"
		case 4:
			ck.Message = "Startet"
		case 5: // present without input power, e.g. an unused RPS input
			ck.Message = "Keine Eingangsspannung"
		case 6:
			ck.State, ck.Message = model.StateCrit, "Liefert keine Leistung"
		case 7:
			ck.State, ck.Message = model.StateWarn, "Netzteil inkompatibel"
		default:
			ck.State, ck.Message = model.StateUnknown, "Status "+strconv.Itoa(state)
		}
		p.add(ck)
	}
}
