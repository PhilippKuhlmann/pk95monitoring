package snmp

import (
	"regexp"
	"strings"

	"pk95monitoring/internal/model"
)

// HOST-RESOURCES-MIB.
const (
	oidHrProcessorLoad = "1.3.6.1.2.1.25.3.3.1.2"
	oidHrStorageEntry  = "1.3.6.1.2.1.25.2.3.1"
	oidHrStorageTypes  = "1.3.6.1.2.1.25.2.1."

	hrStorageType, hrStorageDescr, hrStorageUnits, hrStorageSize, hrStorageUsed = 2, 3, 4, 5, 6

	hrTypeOther, hrTypeRAM, hrTypeFixedDisk = 1, 2, 4
	maxStorages                             = 32
)

// pseudo file systems net-snmp lists as fixed disks.
var pseudoMounts = []string{"/run", "/dev", "/sys", "/proc", "/tmp"}

var winDrive = regexp.MustCompile(`^([A-Za-z]:)\\`)

// hostResources reads CPU (snmp:cpu), memory (snmp:mem) and fixed disks (snmp:storage:…).
func (p *poll) hostResources() {
	if pdus, err := p.s.Walk(oidHrProcessorLoad); err != nil {
		p.partial("CPU", err)
	} else {
		var sum float64
		n := 0
		for _, pdu := range pdus {
			if v, ok := toFloat(pdu); ok {
				sum += v
				n++
			}
		}
		if n > 0 {
			avg := sum / float64(n)
			cores := "1 Kern"
			if n > 1 {
				cores = fmtNum(float64(n), 0) + " Kerne"
			}
			p.add(model.Check{Key: "snmp:cpu", Name: "CPU", Group: "System", State: model.StateOK,
				Message: "Auslastung " + fmtPct(avg) + " (" + cores + ")",
				Metrics: []model.Metric{{Name: "cpu.usage", Value: avg, Unit: "%"}}})
		}
	}

	t, err := walkTable(p.s, oidHrStorageEntry, hrStorageType, hrStorageDescr, hrStorageUnits, hrStorageSize, hrStorageUsed)
	if err != nil {
		p.partial("Speicher", err)
		return
	}
	type entry struct {
		descr       string
		used, total float64
	}
	var ram *entry
	var buffers, cached float64
	var disks []entry
	for _, r := range t.rows {
		typ := hrType(t.str(hrStorageType, r))
		units, _ := t.num(hrStorageUnits, r)
		size, okS := t.num(hrStorageSize, r)
		used, okU := t.num(hrStorageUsed, r)
		if !okS || !okU || units <= 0 {
			continue
		}
		e := entry{descr: t.str(hrStorageDescr, r), used: units * int32u(used), total: units * int32u(size)}
		switch typ {
		case hrTypeRAM:
			if ram == nil && e.total > 0 {
				ram = &e
			}
		case hrTypeOther:
			switch strings.ToLower(e.descr) {
			case "memory buffers":
				buffers = e.used
			case "cached memory":
				cached = e.used
			}
		case hrTypeFixedDisk:
			if e.total > 0 && !pseudoMount(e.descr) {
				disks = append(disks, e)
			}
		}
	}

	if ram != nil {
		used := ram.used
		// net-snmp counts buffers and page cache as used; they are free for applications.
		if buffers+cached < used {
			used -= buffers + cached
		}
		pct := used / ram.total * 100
		p.add(model.Check{Key: "snmp:mem", Name: "Arbeitsspeicher", Group: "System", State: model.StateOK,
			Message: fmtBytes(used) + " von " + fmtBytes(ram.total) + " belegt (" + fmtPct(pct) + ")",
			Metrics: []model.Metric{
				{Name: "mem.usage", Value: pct, Unit: "%"},
				{Name: "mem.used", Value: used, Unit: "B"},
				{Name: "mem.total", Value: ram.total, Unit: "B"},
			}})
	}

	seen := map[string]bool{}
	for _, d := range disks {
		if len(seen) >= maxStorages {
			break
		}
		name := storageName(d.descr)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		pct := d.used / d.total * 100
		p.add(model.Check{Key: "snmp:storage:" + name, Name: "Speicher " + name, Group: "Speicher",
			State:   model.StateOK,
			Message: fmtBytes(d.used) + " von " + fmtBytes(d.total) + " belegt (" + fmtPct(pct) + ")",
			Facts:   map[string]string{"Beschreibung": d.descr},
			Metrics: []model.Metric{
				{Name: "disk.usage", Instance: name, Value: pct, Unit: "%"},
				{Name: "disk.used", Instance: name, Value: d.used, Unit: "B"},
				{Name: "disk.total", Instance: name, Value: d.total, Unit: "B"},
			}})
	}
}

// hrType returns the last sub-identifier of an hrStorageType OID (2 = RAM, 4 = fixed disk).
func hrType(oid string) int {
	rest, ok := strings.CutPrefix(trimDot(oid), oidHrStorageTypes)
	if !ok {
		return 0
	}
	switch rest {
	case "1":
		return hrTypeOther
	case "2":
		return hrTypeRAM
	case "4":
		return hrTypeFixedDisk
	}
	return -1
}

// int32u reads an Integer32 that overflowed (large volumes) as unsigned.
func int32u(v float64) float64 {
	if v < 0 {
		return v + 1<<32
	}
	return v
}

func pseudoMount(descr string) bool {
	if strings.Contains(descr, "/@") { // Synology internal mounts (/volume1/@docker …)
		return true
	}
	for _, m := range pseudoMounts {
		if descr == m || strings.HasPrefix(descr, m+"/") {
			return true
		}
	}
	return false
}

// storageName shortens Windows descriptions ("C:\ Label:System  Serial Number 1a2b") to "C:".
func storageName(descr string) string {
	if m := winDrive.FindStringSubmatch(descr); m != nil {
		return strings.ToUpper(m[1])
	}
	return strings.TrimSpace(descr)
}
