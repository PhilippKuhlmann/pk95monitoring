package snmp

import (
	"strings"

	"pk95monitoring/internal/model"
)

// APC PowerNet-MIB (upsBasic/upsAdv) below 1.3.6.1.4.1.318.1.1.1.
const (
	apcModel         = "1.3.6.1.4.1.318.1.1.1.1.1.1.0"
	apcFirmware      = "1.3.6.1.4.1.318.1.1.1.1.2.1.0"
	apcSerial        = "1.3.6.1.4.1.318.1.1.1.1.2.3.0"
	apcBattStatus    = "1.3.6.1.4.1.318.1.1.1.2.1.1.0" // 2 normal, 3 low, 4 fault
	apcBattCapacity  = "1.3.6.1.4.1.318.1.1.1.2.2.1.0" // %
	apcBattTemp      = "1.3.6.1.4.1.318.1.1.1.2.2.2.0" // °C
	apcBattRuntime   = "1.3.6.1.4.1.318.1.1.1.2.2.3.0" // TimeTicks
	apcBattReplace   = "1.3.6.1.4.1.318.1.1.1.2.2.4.0" // 2 = needs replacing
	apcInputVoltage  = "1.3.6.1.4.1.318.1.1.1.3.2.1.0"
	apcOutputStatus  = "1.3.6.1.4.1.318.1.1.1.4.1.1.0" // 2 onLine, 3 onBattery, …
	apcOutputVoltage = "1.3.6.1.4.1.318.1.1.1.4.2.1.0"
	apcOutputLoad    = "1.3.6.1.4.1.318.1.1.1.4.2.3.0" // %
)

// RFC 1628 UPS-MIB below 1.3.6.1.2.1.33.1.
const (
	upsManufacturer  = "1.3.6.1.2.1.33.1.1.1.0"
	upsModel         = "1.3.6.1.2.1.33.1.1.2.0"
	upsSoftware      = "1.3.6.1.2.1.33.1.1.3.0"
	upsBattStatus    = "1.3.6.1.2.1.33.1.2.1.0" // 2 normal, 3 low, 4 depleted
	upsSecondsOnBatt = "1.3.6.1.2.1.33.1.2.2.0"
	upsMinutesLeft   = "1.3.6.1.2.1.33.1.2.3.0"
	upsCharge        = "1.3.6.1.2.1.33.1.2.4.0" // %
	upsBattTemp      = "1.3.6.1.2.1.33.1.2.7.0"
	upsInputVoltage  = "1.3.6.1.2.1.33.1.3.3.1.3.1"
	upsOutputSource  = "1.3.6.1.2.1.33.1.4.1.0" // 3 normal, 4 bypass, 5 battery, …
	upsOutputVoltage = "1.3.6.1.2.1.33.1.4.4.1.2.1"
	upsOutputLoad    = "1.3.6.1.2.1.33.1.4.4.1.5.1" // %
)

// ups reads APC PowerNet and RFC 1628 values into snmp:ups. APC values win when both
// MIBs answer; states from both are combined.
func (p *poll) ups() {
	v, err := get(p.s, apcModel, apcFirmware, apcSerial, apcBattStatus, apcBattCapacity, apcBattTemp,
		apcBattRuntime, apcBattReplace, apcInputVoltage, apcOutputStatus, apcOutputVoltage, apcOutputLoad,
		upsManufacturer, upsModel, upsSoftware, upsBattStatus, upsSecondsOnBatt, upsMinutesLeft, upsCharge,
		upsBattTemp, upsInputVoltage, upsOutputSource, upsOutputVoltage, upsOutputLoad)
	ck := model.Check{Key: "snmp:ups", Name: "USV", Group: "USV", State: model.StateOK, Facts: map[string]string{}}
	if err != nil {
		p.partial("USV", err)
		ck.State, ck.Message = model.StateUnknown, "Abfrage fehlgeschlagen"
		p.add(ck)
		return
	}
	if len(v) == 0 {
		ck.State, ck.Message = model.StateUnknown, "Keine USV-Daten (UPS-MIB/PowerNet-MIB) gefunden"
		p.add(ck)
		return
	}

	first := func(oids ...string) string {
		for _, o := range oids {
			if s := v.str(o); s != "" {
				return s
			}
		}
		return ""
	}
	num := func(oids ...string) (float64, bool) {
		for _, o := range oids {
			if f, ok := v.num(o); ok {
				return f, true
			}
		}
		return 0, false
	}

	mdl := first(apcModel, upsModel)
	ck.Facts["Hersteller"] = first(upsManufacturer)
	if ck.Facts["Hersteller"] == "" && v[apcModel].Value != nil {
		ck.Facts["Hersteller"] = "APC"
	}
	ck.Facts["Modell"] = mdl
	ck.Facts["Seriennummer"] = first(apcSerial)
	ck.Facts["Firmware"] = first(apcFirmware, upsSoftware)
	for k, val := range ck.Facts {
		if val == "" {
			delete(ck.Facts, k)
		}
	}
	p.facts["Modell"] = mdl
	p.facts["Seriennummer"] = ck.Facts["Seriennummer"]

	var problems []string
	raise := func(state, msg string) {
		ck.State = model.Worst(ck.State, state)
		problems = append(problems, msg)
	}

	apcOut, hasAPCOut := v.int(apcOutputStatus)
	rfcOut, hasRFCOut := v.int(upsOutputSource)
	onBattery := (hasAPCOut && apcOut == 3) || (hasRFCOut && rfcOut == 5)
	if onBattery {
		msg := "Läuft auf Batterie"
		if sec, ok := v.num(upsSecondsOnBatt); ok && sec >= 60 {
			msg += " seit " + fmtUptime(sec)
		}
		raise(model.StateWarn, msg)
	}
	switch {
	case hasAPCOut && (apcOut == 6 || apcOut == 9 || apcOut == 10):
		raise(model.StateWarn, "Bypass aktiv")
	case hasAPCOut && apcOut == 7:
		raise(model.StateCrit, "Ausgang aus")
	case !hasAPCOut && hasRFCOut && rfcOut == 4:
		raise(model.StateWarn, "Bypass aktiv")
	case !hasAPCOut && hasRFCOut && rfcOut == 2:
		raise(model.StateCrit, "Ausgang aus")
	}

	apcBatt, _ := v.int(apcBattStatus)
	rfcBatt, _ := v.int(upsBattStatus)
	switch {
	case rfcBatt == 4:
		raise(model.StateCrit, "Batterie erschöpft")
	case apcBatt == 4:
		raise(model.StateCrit, "Batteriefehler")
	case apcBatt == 3 || rfcBatt == 3:
		raise(model.StateCrit, "Batterie schwach")
	}
	if r, ok := v.int(apcBattReplace); ok && r == 2 {
		raise(model.StateWarn, "Batterie muss getauscht werden")
	}

	var details []string
	add := func(name string, val float64, unit string) {
		ck.Metrics = append(ck.Metrics, model.Metric{Name: name, Value: val, Unit: unit})
	}
	if b, ok := num(apcBattCapacity, upsCharge); ok {
		add("ups.battery", b, "%")
		details = append(details, "Ladung "+fmtPct(b))
	}
	if t, ok := v.num(apcBattRuntime); ok {
		m := t / 100 / 60
		add("ups.runtime", m, "min")
		details = append(details, "Restlaufzeit "+fmtNum(m, 0)+" min")
	} else if m, ok := v.num(upsMinutesLeft); ok {
		add("ups.runtime", m, "min")
		details = append(details, "Restlaufzeit "+fmtNum(m, 0)+" min")
	}
	if l, ok := num(apcOutputLoad, upsOutputLoad); ok {
		add("ups.load", l, "%")
		details = append(details, "Last "+fmtPct(l))
	}
	if in, ok := num(apcInputVoltage, upsInputVoltage); ok {
		add("ups.input", in, "V")
	}
	if out, ok := num(apcOutputVoltage, upsOutputVoltage); ok {
		add("ups.output", out, "V")
	}
	if t, ok := num(apcBattTemp, upsBattTemp); ok {
		ck.Metrics = append(ck.Metrics, model.Metric{Name: "temp", Instance: "Batterie", Value: t, Unit: "°C"})
	}

	summary := "Netzbetrieb"
	if len(problems) > 0 {
		summary = strings.Join(problems, "; ")
	}
	if len(details) > 0 {
		summary += " – " + strings.Join(details, ", ")
	}
	ck.Message = summary
	p.add(ck)
}
