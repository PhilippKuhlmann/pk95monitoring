package snmp

import (
	"strconv"

	"pk95monitoring/internal/model"
)

// SYNOLOGY-SYSTEM-MIB, -DISK-MIB and -RAID-MIB below 1.3.6.1.4.1.6574.
const (
	synoSystemStatus = "1.3.6.1.4.1.6574.1.1.0" // 1 normal, 2 failed
	synoTemperature  = "1.3.6.1.4.1.6574.1.2.0" // °C
	synoPowerStatus  = "1.3.6.1.4.1.6574.1.3.0"
	synoSystemFan    = "1.3.6.1.4.1.6574.1.4.1.0"
	synoCPUFan       = "1.3.6.1.4.1.6574.1.4.2.0"
	synoSerial       = "1.3.6.1.4.1.6574.1.5.2.0"
	synoVersion      = "1.3.6.1.4.1.6574.1.5.3.0"
	synoUpgrade      = "1.3.6.1.4.1.6574.1.5.4.0" // 1 = update available

	synoDiskEntry = "1.3.6.1.4.1.6574.2.1.1"
	synoRaidEntry = "1.3.6.1.4.1.6574.3.1.1"

	diskID, diskModel, diskType, diskStatus, diskTemp, diskHealth = 2, 3, 4, 5, 6, 13
	raidName, raidStatus                                          = 2, 3
)

var synoDiskStates = map[int]struct{ state, msg string }{
	1: {model.StateOK, "Normal"},
	2: {model.StateOK, "Initialisiert"},
	3: {model.StateWarn, "Nicht initialisiert"},
	4: {model.StateCrit, "Systempartition fehlerhaft"},
	5: {model.StateCrit, "Defekt"},
}

var synoDiskHealth = map[int]struct{ state, msg string }{
	2: {model.StateWarn, "Gesundheit: Warnung"},
	3: {model.StateCrit, "Gesundheit: kritisch"},
	4: {model.StateCrit, "Gesundheit: fällt aus"},
}

var synoRaidStates = map[int]struct{ state, msg string }{
	1:  {model.StateOK, "Normal"},
	2:  {model.StateWarn, "Wird repariert"},
	3:  {model.StateOK, "Wird migriert"},
	4:  {model.StateOK, "Wird erweitert"},
	5:  {model.StateOK, "Wird gelöscht"},
	6:  {model.StateOK, "Wird erstellt"},
	7:  {model.StateOK, "Wird synchronisiert"},
	8:  {model.StateOK, "Paritätsprüfung"},
	9:  {model.StateOK, "Wird zusammengesetzt"},
	10: {model.StateOK, "Wird abgebrochen"},
	11: {model.StateCrit, "Beeinträchtigt (degraded)"},
	12: {model.StateCrit, "Abgestürzt"},
	13: {model.StateOK, "Datenbereinigung"},
	21: {model.StateUnknown, "Status unbekannt"},
}

// nas reads the Synology MIBs: system state into snmp:system, snmp:temp:System,
// snmp:disk:<id> and snmp:raid:<name>.
func (p *poll) nas() {
	v, err := get(p.s, oidSynoModel, synoSystemStatus, synoTemperature, synoPowerStatus, synoSystemFan,
		synoCPUFan, synoSerial, synoVersion, synoUpgrade)
	if err != nil {
		p.partial("Synology", err)
		return
	}
	if len(v) == 0 {
		p.problem(model.StateUnknown, "Keine Synology-Daten (SYNOLOGY-SYSTEM-MIB) gefunden")
		return
	}
	p.facts["Modell"] = v.str(oidSynoModel)
	p.facts["Seriennummer"] = v.str(synoSerial)
	p.facts["DSM-Version"] = v.str(synoVersion)
	if u, ok := v.int(synoUpgrade); ok && u == 1 {
		p.facts["Update"] = "verfügbar"
	}
	if s, ok := v.int(synoSystemStatus); ok && s == 2 {
		p.problem(model.StateCrit, "Systemstatus: Fehler")
	}
	if s, ok := v.int(synoPowerStatus); ok && s == 2 {
		p.problem(model.StateCrit, "Netzteilfehler")
	}
	fan1, _ := v.int(synoSystemFan)
	fan2, _ := v.int(synoCPUFan)
	if fan1 == 2 || fan2 == 2 {
		p.problem(model.StateWarn, "Lüfterfehler")
	}
	if t, ok := v.num(synoTemperature); ok {
		p.add(model.Check{Key: "snmp:temp:System", Name: "Temperatur System", Group: "Sensoren",
			State: model.StateOK, Message: fmtNum(t, 1) + " °C",
			Metrics: []model.Metric{{Name: "temp", Instance: "System", Value: t, Unit: "°C"}}})
	}

	if t, err := walkTable(p.s, synoDiskEntry, diskID, diskModel, diskType, diskStatus, diskTemp, diskHealth); err != nil {
		p.partial("Platten", err)
	} else {
		for _, r := range t.rows {
			id := t.str(diskID, r)
			if id == "" {
				id = "Disk " + r
			}
			ck := model.Check{Key: "snmp:disk:" + id, Name: "Platte " + id, Group: "Platten",
				State: model.StateUnknown, Message: "Status unbekannt", Facts: map[string]string{}}
			if s, ok := t.int(diskStatus, r); ok {
				if ds, ok := synoDiskStates[s]; ok {
					ck.State, ck.Message = ds.state, ds.msg
				} else {
					ck.Message = "Status " + strconv.Itoa(s)
				}
			}
			if h, ok := t.int(diskHealth, r); ok {
				if hs, ok := synoDiskHealth[h]; ok {
					ck.State = model.Worst(ck.State, hs.state)
					ck.Message += ", " + hs.msg
				}
			}
			if m := t.str(diskModel, r); m != "" {
				ck.Facts["Modell"] = m
			}
			if ty := t.str(diskType, r); ty != "" {
				ck.Facts["Typ"] = ty
			}
			if temp, ok := t.num(diskTemp, r); ok && temp > 0 {
				ck.Metrics = []model.Metric{{Name: "temp", Instance: id, Value: temp, Unit: "°C"}}
				ck.Message += ", " + fmtNum(temp, 0) + " °C"
			}
			p.add(ck)
		}
	}

	if t, err := walkTable(p.s, synoRaidEntry, raidName, raidStatus); err != nil {
		p.partial("RAID", err)
	} else {
		for _, r := range t.rows {
			name := t.str(raidName, r)
			if name == "" {
				name = "RAID " + r
			}
			ck := model.Check{Key: "snmp:raid:" + name, Name: "RAID " + name, Group: "Speicher",
				State: model.StateUnknown, Message: "Status unbekannt"}
			if s, ok := t.int(raidStatus, r); ok {
				if rs, ok := synoRaidStates[s]; ok {
					ck.State, ck.Message = rs.state, rs.msg
				} else {
					ck.State, ck.Message = model.StateOK, "Status "+strconv.Itoa(s)
				}
			}
			p.add(ck)
		}
	}
}
