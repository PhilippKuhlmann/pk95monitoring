package snmp

import (
	"strings"

	"pk95monitoring/internal/model"
)

// Printer-MIB (RFC 3805) and the printer part of HOST-RESOURCES-MIB.
const (
	oidSuppliesEntry   = "1.3.6.1.2.1.43.11.1.1"
	oidMarkerLifeCount = "1.3.6.1.2.1.43.10.2.1.4"
	oidPrtSerial       = "1.3.6.1.2.1.43.5.1.1.17." // + hrDeviceIndex
	oidHrPrinterStatus = "1.3.6.1.2.1.25.3.5.1.1"   // column, index hrDeviceIndex
	oidHrPrinterError  = "1.3.6.1.2.1.25.3.5.1.2."  // hrPrinterDetectedErrorState
	oidHrDeviceDescr   = "1.3.6.1.2.1.25.3.2.1.3."  // + hrDeviceIndex
	oidHrDeviceStatus  = "1.3.6.1.2.1.25.3.2.1.5."  // 2 running, 3 warning, 5 down
	supType, supDescr  = 5, 6
	supMax, supLevel   = 8, 9
	levelSomeRemaining = -3
)

// printerErrors are the bits of hrPrinterDetectedErrorState (bit 0 = MSB of the first byte).
var printerErrors = []struct {
	state, msg string
}{
	{model.StateWarn, "Papier fast leer"},
	{model.StateCrit, "Kein Papier"},
	{model.StateWarn, "Toner fast leer"},
	{model.StateCrit, "Toner leer"},
	{model.StateWarn, "Klappe offen"},
	{model.StateCrit, "Papierstau"},
	{model.StateWarn, "Offline"},
	{model.StateWarn, "Wartung erforderlich"},
	{model.StateWarn, "Papierfach fehlt"},
	{model.StateWarn, "Ausgabefach fehlt"},
	{model.StateCrit, "Verbrauchsmaterial fehlt"},
	{model.StateWarn, "Ausgabefach fast voll"},
	{model.StateWarn, "Ausgabefach voll"},
	{model.StateWarn, "Papierfach leer"},
	{model.StateWarn, "Wartung überfällig"},
}

var supplyTypes = map[int]string{
	3: "Toner", 4: "Resttonerbehälter", 5: "Tinte", 6: "Tintenpatrone", 7: "Tintenband",
	9: "Trommel", 10: "Entwickler", 15: "Fixiereinheit", 21: "Transfereinheit",
}

// printer produces snmp:printer (state, page counter) and snmp:toner:<description>.
func (p *poll) printer() {
	ck := model.Check{Key: "snmp:printer", Name: "Drucker", Group: "Drucker", State: model.StateOK,
		Facts: map[string]string{}}
	idx := "1"
	if pdus, err := p.s.Walk(oidHrPrinterStatus); err == nil {
		for _, pdu := range pdus {
			if i, ok := strings.CutPrefix(trimDot(pdu.Name), oidHrPrinterStatus+"."); ok {
				idx = i
				break
			}
		}
	}
	v, err := get(p.s, oidHrPrinterStatus+"."+idx, oidHrPrinterError+idx, oidHrDeviceDescr+idx,
		oidHrDeviceStatus+idx, oidPrtSerial+idx)
	if err != nil {
		p.partial("Drucker", err)
		ck.State, ck.Message = model.StateUnknown, "Abfrage fehlgeschlagen"
		p.add(ck)
		return
	}
	if d := v.str(oidHrDeviceDescr + idx); d != "" {
		ck.Facts["Modell"] = d
		p.facts["Modell"] = d
	}
	if sn := v.str(oidPrtSerial + idx); sn != "" {
		ck.Facts["Seriennummer"] = sn
		p.facts["Seriennummer"] = sn
	}

	var problems []string
	if pdu, ok := v[oidHrPrinterError+idx]; ok {
		if b, ok := pdu.Value.([]byte); ok {
			for _, bit := range errorBits(b) {
				if bit < len(printerErrors) {
					ck.State = model.Worst(ck.State, printerErrors[bit].state)
					problems = append(problems, printerErrors[bit].msg)
				}
			}
		}
	}
	devStatus, _ := v.int(oidHrDeviceStatus + idx)
	switch {
	case devStatus == 5 && ck.State != model.StateCrit:
		ck.State = model.StateCrit
		problems = append(problems, "Gerät gestört")
	case devStatus == 3 && len(problems) == 0:
		ck.State = model.StateWarn
		problems = append(problems, "Warnung")
	}
	switch {
	case len(v) == 0:
		ck.State, ck.Message = model.StateUnknown, "Kein Druckerstatus (HOST-RESOURCES-MIB) gefunden"
	case len(problems) > 0:
		ck.Message = strings.Join(problems, ", ")
	default:
		ck.Message = "Bereit"
		if st, ok := v.int(oidHrPrinterStatus + "." + idx); ok {
			switch st {
			case 4:
				ck.Message = "Druckt"
			case 5:
				ck.Message = "Aufwärmen"
			}
		}
	}

	if pdus, err := p.s.Walk(oidMarkerLifeCount); err != nil {
		p.partial("Seitenzähler", err)
	} else {
		for _, pdu := range pdus {
			if n, ok := toFloat(pdu); ok {
				ck.Metrics = append(ck.Metrics, model.Metric{Name: "printer.pages", Value: n})
				ck.Facts["Seitenzähler"] = fmtNum(n, 0)
				break
			}
		}
	}
	p.add(ck)
	p.supplies()
}

// errorBits returns the set bit numbers of an SNMP BITS value.
func errorBits(b []byte) []int {
	var bits []int
	for i, c := range b {
		for j := range 8 {
			if c&(0x80>>j) != 0 {
				bits = append(bits, i*8+j)
			}
		}
	}
	return bits
}

// supplies produces snmp:toner:<description> with printer.toner (% remaining).
func (p *poll) supplies() {
	t, err := walkTable(p.s, oidSuppliesEntry, supDescr, supType, supMax, supLevel)
	if err != nil {
		p.partial("Verbrauchsmaterial", err)
		return
	}
	seen := map[string]bool{}
	for _, r := range t.rows {
		name := t.str(supDescr, r)
		if name == "" {
			name = "Verbrauchsmaterial " + r
		}
		if seen[strings.ToLower(name)] {
			name += " (" + r + ")"
		}
		seen[strings.ToLower(name)] = true

		label := name // "Magenta Toner" already says what it is; "Black" alone does not
		if !strings.Contains(strings.ToLower(name), "toner") {
			label = "Toner " + name
		}
		ck := model.Check{Key: "snmp:toner:" + name, Name: label, Group: "Drucker", State: model.StateOK}
		if typ, ok := t.int(supType, r); ok {
			if s, ok := supplyTypes[typ]; ok {
				ck.Facts = map[string]string{"Typ": s}
			}
		}
		maxCap, okMax := t.num(supMax, r)
		level, okLevel := t.num(supLevel, r)
		switch {
		case okLevel && level == levelSomeRemaining:
			ck.Message = "Noch vorhanden"
		case okLevel && level >= 0 && okMax && maxCap > 0:
			pct := min(max(level*100/maxCap, 0), 100)
			ck.Message = fmtPct(pct)
			if level == 0 {
				ck.Message = "Leer"
			}
			ck.Metrics = []model.Metric{{Name: "printer.toner", Instance: name, Value: pct, Unit: "%"}}
		default:
			ck.State = model.StateUnknown
			ck.Message = "Füllstand unbekannt"
		}
		p.add(ck)
	}
}
