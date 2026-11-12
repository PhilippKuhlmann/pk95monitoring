package fakesnmp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// sysDesc is the SNMPv2-MIB system group of a device.
type sysDesc struct{ descr, objectID, name, location, contact string }

func (d *Device) buildSystem(t float64, m mib, s sysDesc) {
	const sys = "1.3.6.1.2.1.1."
	m.str(sys+"1.0", s.descr)
	m.oid(sys+"2.0", s.objectID)
	m.ticks(sys+"3.0", d.baseUptime+t)
	m.str(sys+"4.0", s.contact)
	m.str(sys+"5.0", s.name)
	m.str(sys+"6.0", s.location)
	m.int(sys+"7.0", 72)
}

// port is one interface with simulated traffic.
type port struct {
	index              int
	name, descr, alias string
	typ                int
	speed              float64 // bit/s
	up                 bool
	disabled           bool    // ifAdminStatus down (the link is then down as well)
	rxAvg, txAvg       float64 // B/s
	period, phRx, phTx float64
	baseRx, baseTx     float64
	errRate            float64 // errors per second while up
	downAt             float64 // elapsed seconds when the port went down (while !up)
	lostRx, lostTx     float64 // traffic missed in earlier down periods
	lostUp             float64 // seconds of earlier down periods
}

// eff is the time up to which the port has been passing traffic.
func (p *port) eff(t float64) float64 {
	if p.up {
		return t
	}
	return min(p.downAt, t)
}

func (p *port) octets(t float64) (rx, tx float64) {
	e := p.eff(t)
	rx = p.baseRx + integral(p.rxAvg, p.period, p.phRx, e) - p.lostRx
	tx = p.baseTx + integral(p.txAvg, p.period, p.phTx, e) - p.lostTx
	return rx, tx
}

// addPort adds an interface; avg is the mean receive rate in B/s (transmit is 40 % of it).
func (d *Device) addPort(index int, name, descr, alias string, typ int, speed float64, up bool, avg float64) *port {
	r := d.rnd(uint64(index) * 7919)
	p := &port{
		index: index, name: name, descr: descr, alias: alias, typ: typ, speed: speed, up: up,
		rxAvg: avg, txAvg: avg * (0.3 + float64(r%50)/100),
		period: 3600 * float64(2+r%6),
		phRx:   float64(r%628) / 100, phTx: float64((r/7)%628) / 100,
		baseRx: float64(r % 1e12), baseTx: float64((r / 3) % 1e12),
	}
	d.ports = append(d.ports, p)
	return p
}

// SetPortUp sets the link state of the interface with the given ifIndex (switch ports
// are numbered from 1). Traffic counters stop while a port is down. A disabled port
// (SetPortEnabled) stays down.
func (d *Device) SetPortUp(index int, up bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setLink(index, up)
}

// SetPortEnabled sets the administrative state (ifAdminStatus) of the interface with the
// given ifIndex. Disabling takes the link down; enabling leaves it down until SetPortUp.
func (d *Device) SetPortEnabled(index int, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.ports {
		if p.index == index {
			p.disabled = !enabled
		}
	}
	if !enabled {
		d.setLink(index, false)
	}
}

// setLink changes the link state; the caller holds d.mu.
func (d *Device) setLink(index int, up bool) {
	t := d.elapsed()
	for _, p := range d.ports {
		if p.index != index || p.up == up || (up && p.disabled) {
			continue
		}
		if !up {
			p.downAt = t
		} else {
			p.lostRx += integral(p.rxAvg, p.period, p.phRx, t) - integral(p.rxAvg, p.period, p.phRx, p.downAt)
			p.lostTx += integral(p.txAvg, p.period, p.phTx, t) - integral(p.txAvg, p.period, p.phTx, p.downAt)
			p.lostUp += t - p.downAt
		}
		p.up = up
	}
}

func (d *Device) buildPorts(t float64, m mib) {
	const e, x = "1.3.6.1.2.1.2.2.1.", "1.3.6.1.2.1.31.1.1.1."
	m.int("1.3.6.1.2.1.2.1.0", len(d.ports))
	for _, p := range d.ports {
		i := "." + strconv.Itoa(p.index)
		oper, admin := 2, 1
		if p.up {
			oper = 1
		}
		if p.disabled {
			admin = 2
		}
		rx, tx := p.octets(t)
		errs := p.errRate * (p.eff(t) - p.lostUp)
		m.int(e+"1"+i, p.index)
		m.str(e+"2"+i, p.descr)
		m.int(e+"3"+i, p.typ)
		m.gauge(e+"5"+i, p.speed)
		m.int(e+"7"+i, admin)
		m.int(e+"8"+i, oper)
		m.c32(e+"10"+i, rx)
		m.c32(e+"14"+i, math.Floor(errs*0.7))
		m.c32(e+"16"+i, tx)
		m.c32(e+"20"+i, math.Floor(errs*0.3))
		m.str(x+"1"+i, p.name)
		m.c64(x+"6"+i, rx)
		m.c64(x+"10"+i, tx)
		m.gauge(x+"15"+i, p.speed/1e6)
		m.str(x+"18"+i, p.alias)
	}
}

// rnd is a deterministic pseudo-random number per device and salt.
func (d *Device) rnd(salt uint64) uint64 {
	x := d.seed + salt*0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// int32v stores a value of an Integer32 column the way large devices overflow it.
func int32v(v float64) int { return int(int32(uint32(uint64(v)))) }

// ---- switch ----

// NewSwitch returns a managed switch with the given number of gigabit access ports
// (ifIndex 1…n, ifName "Gi1/0/<n>"), a VLAN interface and a loopback. The last quarter of
// the access ports has no link; with 8 or more ports the last one is disabled
// (ifAdminStatus down) and there are two 10-gigabit uplinks (ifIndex n+1, n+2, ifName
// "Te1/1/1", "Te1/1/2", both up). Links never change by themselves (SetPortUp,
// SetPortEnabled). It has no vendor MIB, so "auto" detects it by its Ethernet ports.
// BRIDGE-MIB maps base port n to ifIndex n; LLDP numbers its local ports 500+ifIndex with
// lldpLocPortId = ifName and has neighbors on Gi1/0/1 (fw-01), Gi1/0/2–4 (pve1–3) and,
// with 8 or more ports, Te1/1/1 (sw-kern). A neighbor disappears while its port is down.
func NewSwitch(ports int) *Device {
	ports = max(ports, 1)
	d := newDevice(fmt.Sprintf("switch-%d", ports))
	sys := sysDesc{
		descr:    fmt.Sprintf("PK95 Demo Switch %dG, Firmware 2.4.1", ports),
		objectID: "1.3.6.1.4.1.32767.1.1", name: "sw-core", location: "Serverraum", contact: "it@example.com",
	}
	aliases := map[int]string{1: "Uplink Router", 2: "pve1", 3: "pve2", 4: "pve3", 5: "NAS", 6: "USV"}
	downFrom := ports - ports/4 + 1
	for i := 1; i <= ports; i++ {
		avg := 20e3 + float64(d.rnd(uint64(i))%1500)*1e3
		if i == 1 {
			avg = 12e6
		}
		p := d.addPort(i, fmt.Sprintf("Gi1/0/%d", i), fmt.Sprintf("GigabitEthernet1/0/%d", i), aliases[i], 6, 1e9, i < downFrom, avg)
		if i == 3 {
			p.errRate = 0.2
		}
		if i == ports && ports >= 8 {
			p.disabled = true
		}
	}
	if ports >= 8 {
		d.addPort(ports+1, "Te1/1/1", "TenGigabitEthernet1/1/1", "Uplink Kern-Switch", 6, 10e9, true, 25e6)
		d.addPort(ports+2, "Te1/1/2", "TenGigabitEthernet1/1/2", "Uplink Kern-Switch (redundant)", 6, 10e9, true, 400e3)
	}
	d.addPort(1001, "vlan1", "Vlan1", "Management", 53, 1e9, true, 2e3)
	d.addPort(1002, "lo", "Loopback", "", 24, 0, true, 0)
	// LLDP numbers the local ports 501…; lldpLocPortId is the ifName (as on Cisco).
	d.sw.locPortBase, d.sw.locByName = 500, true
	d.sw.bridgePorts = ports + 2
	d.sw.neighbors = []lldpNeighbor{
		{ifIndex: 1, timeMark: 0, sysName: "fw-01", portID: "port1", portSub: 5, portDesc: "LAN"},
		{ifIndex: 2, timeMark: 1200, sysName: "pve1", portID: "eno1", portSub: 5},
		{ifIndex: 3, timeMark: 1200, sysName: "pve2", portID: "eno1", portSub: 5},
		{ifIndex: 4, timeMark: 1300, sysName: "pve3", portID: "eno1", portSub: 5},
	}
	if ports >= 8 {
		d.sw.neighbors = append(d.sw.neighbors,
			lldpNeighbor{ifIndex: ports + 1, timeMark: 0, sysName: "sw-kern", portID: "Te1/0/1", portSub: 5,
				portDesc: "TenGigabitEthernet1/0/1"})
	}
	for i := range d.sw.neighbors {
		d.sw.neighbors[i].chassis = d.mac(uint64(100 + i))
	}
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildSwitchMIBs(t, m)
		m.int("1.3.6.1.2.1.25.3.3.1.2.1", int(math.Round(12+6*d.wave(t, 3600, 1)+2*d.noise(t, 2))))
	}
	return d
}

// ---- UPS ----

type upsState struct {
	onBattery          bool
	switchAt, capAtSwi float64 // time of the last mains/battery switch and capacity then
	low, replace       bool
}

// NewUPS returns an APC-style UPS that answers both the APC PowerNet-MIB and the
// RFC 1628 UPS-MIB. The battery drains while on battery and recharges on mains.
func NewUPS() *Device {
	d := newDevice("ups")
	d.ups.capAtSwi = 100
	sys := sysDesc{
		descr:    "APC Web/SNMP Management Card (Demo) Smart-UPS 1500",
		objectID: "1.3.6.1.4.1.318.1.3.27", name: "usv-01", location: "Serverraum", contact: "it@example.com",
	}
	d.addPort(1, "eth0", "eth0", "", 6, 100e6, true, 3e3)
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildUPS(t, m)
	}
	return d
}

// SetOnBattery switches between mains (false) and battery (true).
func (d *Device) SetOnBattery(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ups.onBattery == on {
		return
	}
	t := d.elapsed()
	d.ups.capAtSwi = d.capacity(t)
	d.ups.switchAt = t
	d.ups.onBattery = on
}

// SetBatteryLow forces the "battery low" status.
func (d *Device) SetBatteryLow(low bool) {
	d.mu.Lock()
	d.ups.low = low
	d.mu.Unlock()
}

// SetReplaceBattery sets APC's "battery needs replacing" indicator.
func (d *Device) SetReplaceBattery(replace bool) {
	d.mu.Lock()
	d.ups.replace = replace
	d.mu.Unlock()
}

// capacity is the battery charge in % (drains 1.5 %/min on battery, charges 1 %/min).
func (d *Device) capacity(t float64) float64 {
	dt := (t - d.ups.switchAt) / 60
	if d.ups.onBattery {
		return max(3, d.ups.capAtSwi-1.5*dt)
	}
	return min(100, d.ups.capAtSwi+dt)
}

func (d *Device) buildUPS(t float64, m mib) {
	u := d.ups
	capacity := d.capacity(t)
	load := 28 + 5*d.wave(t, 1800, 1) + 1.5*d.noise(t, 2)
	runtime := capacity / 100 * 75 * 30 / load // minutes
	low := u.low || capacity < 25
	in := 230 + 2*d.wave(t, 7200, 3) + d.noise(t, 4)
	if u.onBattery {
		in = 0
	}
	out := 230 + 0.5*d.noise(t, 5)
	temp := 27 + 1.5*d.wave(t, 10800, 6) + 0.3*d.noise(t, 7)
	battStatus, apcOut, rfcOut, replace, onBattSec := 2, 2, 3, 1, 0
	if low {
		battStatus = 3
	}
	if u.onBattery {
		apcOut, rfcOut, onBattSec = 3, 5, int(t-u.switchAt)
	}
	if u.replace {
		replace = 2
	}

	const a = "1.3.6.1.4.1.318.1.1.1."
	m.str(a+"1.1.1.0", "Smart-UPS 1500 (Demo)")
	m.str(a+"1.1.2.0", "usv-01")
	m.str(a+"1.2.1.0", "UPS 09.3 (ID18)")
	m.str(a+"1.2.3.0", "AS1234567890")
	m.int(a+"2.1.1.0", battStatus)
	m.gauge(a+"2.2.1.0", math.Round(capacity))
	m.gauge(a+"2.2.2.0", math.Round(temp))
	m.ticks(a+"2.2.3.0", runtime*60)
	m.int(a+"2.2.4.0", replace)
	m.gauge(a+"3.2.1.0", math.Round(in))
	m.int(a+"4.1.1.0", apcOut)
	m.gauge(a+"4.2.1.0", math.Round(out))
	m.gauge(a+"4.2.3.0", math.Round(load))

	const r = "1.3.6.1.2.1.33.1."
	m.str(r+"1.1.0", "APC")
	m.str(r+"1.2.0", "Smart-UPS 1500 (Demo)")
	m.str(r+"1.3.0", "UPS 09.3")
	m.int(r+"2.1.0", battStatus)
	m.int(r+"2.2.0", onBattSec)
	m.int(r+"2.3.0", int(math.Round(runtime)))
	m.int(r+"2.4.0", int(math.Round(capacity)))
	m.int(r+"2.7.0", int(math.Round(temp)))
	m.int(r+"3.2.0", 1)
	m.int(r+"3.3.1.3.1", int(math.Round(in)))
	m.int(r+"4.1.0", rfcOut)
	m.int(r+"4.3.0", 1)
	m.int(r+"4.4.1.2.1", int(math.Round(out)))
	m.int(r+"4.4.1.5.1", int(math.Round(load)))
}

// ---- printer ----

type supply struct {
	descr string
	typ   int     // 3 toner, 4 waste toner
	base  float64 // level in % at start
	fixed *int    // level set by SetTonerLevel (or -3 "some remaining")
}

type printerState struct {
	supplies       []*supply
	paperOut, jam  bool
	basePages      float64
	pagesPerSecond float64
}

// NewPrinter returns a color laser printer with CMYK toners (magenta is low), a waste
// toner box that only reports "some remaining", and a growing page counter.
func NewPrinter() *Device {
	d := newDevice("printer")
	sys := sysDesc{
		descr:    "PK95 Demo Color Laser MFP",
		objectID: "1.3.6.1.4.1.32767.2.1", name: "drucker-buero", location: "Büro EG", contact: "it@example.com",
	}
	waste := -3
	d.printer = printerState{
		basePages:      float64(15000 + d.seed%20000),
		pagesPerSecond: 0.04,
		supplies: []*supply{
			{descr: "Black Toner", typ: 3, base: 64},
			{descr: "Cyan Toner", typ: 3, base: 45},
			{descr: "Magenta Toner", typ: 3, base: 7},
			{descr: "Yellow Toner", typ: 3, base: 72},
			{descr: "Waste Toner Box", typ: 4, fixed: &waste},
		},
	}
	d.addPort(1, "eth0", "Ethernet", "", 6, 100e6, true, 1e3)
	d.addPort(2, "lo0", "Loopback", "", 24, 0, true, 0)
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildPrinter(t, m)
	}
	return d
}

// SetTonerLevel fixes the level (in %, or -3 "some remaining", -2 unknown) of the supplies
// whose description contains name (case-insensitive), e.g. "cyan".
func (d *Device) SetTonerLevel(name string, level int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.printer.supplies {
		if strings.Contains(strings.ToLower(s.descr), strings.ToLower(name)) {
			s.fixed = &level
		}
	}
}

// SetPaperOut sets the "no paper" error.
func (d *Device) SetPaperOut(out bool) {
	d.mu.Lock()
	d.printer.paperOut = out
	d.mu.Unlock()
}

// SetJam sets the "paper jam" error.
func (d *Device) SetJam(jam bool) {
	d.mu.Lock()
	d.printer.jam = jam
	d.mu.Unlock()
}

func (d *Device) buildPrinter(t float64, m mib) {
	ps := d.printer
	printed := integral(ps.pagesPerSecond, 7200, 1, t)
	var bits [2]byte
	set := func(n int) { bits[n/8] |= 0x80 >> (n % 8) }

	const sup = "1.3.6.1.2.1.43.11.1.1."
	for i, s := range ps.supplies {
		idx := ".1." + strconv.Itoa(i+1)
		level := 0
		if s.fixed != nil {
			level = *s.fixed
		} else {
			l := s.base - printed/400 // 1 % per 400 pages, replaced when empty
			for l < 0 {
				l += 100
			}
			level = int(math.Ceil(l))
		}
		if s.typ == 3 && level >= 0 && level <= 10 {
			set(2) // lowToner
		}
		if s.typ == 3 && level == 0 {
			set(3) // noToner
		}
		class := 3
		if s.typ == 4 {
			class = 4
		}
		m.int(sup+"4"+idx, class)
		m.int(sup+"5"+idx, s.typ)
		m.str(sup+"6"+idx, s.descr)
		m.int(sup+"7"+idx, 19) // percent
		m.int(sup+"8"+idx, 100)
		m.int(sup+"9"+idx, level)
	}
	if ps.paperOut {
		set(1)
	}
	if ps.jam {
		set(5)
	}
	devStatus, prtStatus := 2, 3
	if bits != [2]byte{} {
		devStatus, prtStatus = 3, 1
	} else if d.wave(t, 900, 9) > 0.6 {
		prtStatus = 4
	}
	if ps.jam {
		devStatus = 5
	}

	m.oid("1.3.6.1.2.1.25.3.2.1.2.1", "1.3.6.1.2.1.25.3.1.5")
	m.str("1.3.6.1.2.1.25.3.2.1.3.1", "PK95 Demo Color Laser MFP")
	m.int("1.3.6.1.2.1.25.3.2.1.5.1", devStatus)
	m.int("1.3.6.1.2.1.25.3.5.1.1.1", prtStatus)
	m.bytes("1.3.6.1.2.1.25.3.5.1.2.1", bits[:])
	m.c32("1.3.6.1.2.1.43.5.1.1.1.1", 3)
	m.str("1.3.6.1.2.1.43.5.1.1.17.1", "DEMO-PRN-4711")
	m.c32("1.3.6.1.2.1.43.10.2.1.4.1.1", ps.basePages+math.Floor(printed))
}

// ---- Synology NAS ----

type nasState struct {
	diskStatus []int // per disk, 1 = normal
	raidStatus []int // per RAID, 1 = normal
	failed     bool
}

var nasRaids = []string{"Volume 1", "Storage Pool 1"}

// NewNAS returns a Synology NAS (net-snmp sysObjectID plus the Synology MIBs) with four
// disks, one volume, HOST-RESOURCES-MIB (CPU, memory, /volume1) and two Ethernet ports.
func NewNAS() *Device {
	d := newDevice("nas")
	d.nas = nasState{diskStatus: []int{1, 1, 1, 1}, raidStatus: []int{1, 1}}
	sys := sysDesc{
		descr:    "Linux nas01 4.4.302+ #72806 SMP x86_64",
		objectID: "1.3.6.1.4.1.8072.3.2.10", name: "nas01", location: "Serverraum", contact: "it@example.com",
	}
	d.addPort(1, "eth0", "eth0", "", 6, 1e9, true, 4e6)
	d.addPort(2, "eth1", "eth1", "", 6, 1e9, true, 5e5)
	d.addPort(3, "lo", "lo", "", 24, 0, true, 1e4)
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildHost(t, m)
		d.buildSynology(t, m)
	}
	return d
}

// SetSystemFailed sets the Synology system status to failed.
func (d *Device) SetSystemFailed(failed bool) {
	d.mu.Lock()
	d.nas.failed = failed
	d.mu.Unlock()
}

// SetDiskStatus sets the Synology status of disk n (1…4): 1 normal, 3 not initialized,
// 4 system partition failed, 5 crashed.
func (d *Device) SetDiskStatus(n, status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n >= 1 && n <= len(d.nas.diskStatus) {
		d.nas.diskStatus[n-1] = status
	}
}

// SetRaidStatus sets the status of all RAIDs: 1 normal, 2 repairing, 11 degraded, 12 crashed.
func (d *Device) SetRaidStatus(status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.nas.raidStatus {
		d.nas.raidStatus[i] = status
	}
}

func (d *Device) buildHost(t float64, m mib) {
	m.ticks("1.3.6.1.2.1.25.1.1.0", d.baseUptime+t)
	for i := 1; i <= 4; i++ {
		load := 14 + 10*d.wave(t, 2400, uint64(10+i)) + 4*d.noise(t, uint64(20+i))
		m.int("1.3.6.1.2.1.25.3.3.1.2."+strconv.Itoa(196607+i), int(math.Round(max(load, 0))))
	}
	const st = "1.3.6.1.2.1.25.2.3.1."
	const types = "1.3.6.1.2.1.25.2.1."
	ram := 8.0 * (1 << 30)
	vol := 10.5e12
	rows := []struct {
		idx         int
		typ, descr  string
		units       float64
		size, usedF float64
	}{
		{1, "2", "Physical memory", 1024, ram, 0.86 + 0.03*d.wave(t, 5400, 30)},
		{3, "3", "Virtual memory", 1024, ram + 4*(1<<30), 0.6},
		{6, "1", "Memory buffers", 1024, ram, 0.05},
		{7, "1", "Cached memory", 1024, ram, 0.45},
		{31, "4", "/", 4096, 2.3e9, 0.58},
		{32, "4", "/run", 4096, 1.2e9, 0.01},
		{41, "4", "/volume1", 4096, vol, 0.62 + 0.0000001*t},
	}
	for _, r := range rows {
		i := "." + strconv.Itoa(r.idx)
		m.int(st+"1"+i, r.idx)
		m.oid(st+"2"+i, types+r.typ)
		m.str(st+"3"+i, r.descr)
		m.int(st+"4"+i, int(r.units))
		m.int(st+"5"+i, int32v(r.size/r.units))
		m.int(st+"6"+i, int32v(r.size*min(r.usedF, 1)/r.units))
	}
}

func (d *Device) buildSynology(t float64, m mib) {
	const s = "1.3.6.1.4.1.6574."
	status := 1
	if d.nas.failed {
		status = 2
	}
	m.int(s+"1.1.0", status)
	m.int(s+"1.2.0", int(math.Round(41+3*d.wave(t, 7200, 40)+d.noise(t, 41))))
	m.int(s+"1.3.0", 1)
	m.int(s+"1.4.1.0", 1)
	m.int(s+"1.4.2.0", 1)
	m.str(s+"1.5.1.0", "DS920+ (Demo)")
	m.str(s+"1.5.2.0", "2080PDN123456")
	m.str(s+"1.5.3.0", "DSM 7.2.2-72806")
	m.int(s+"1.5.4.0", 2)
	for i, ds := range d.nas.diskStatus {
		idx := "." + strconv.Itoa(i)
		health := 1
		if ds >= 4 {
			health = 3
		}
		m.int(s+"2.1.1.1"+idx, i)
		m.str(s+"2.1.1.2"+idx, "Disk "+strconv.Itoa(i+1))
		m.str(s+"2.1.1.3"+idx, "WD40EFRX-68N32N0")
		m.str(s+"2.1.1.4"+idx, "SATA")
		m.int(s+"2.1.1.5"+idx, ds)
		m.int(s+"2.1.1.6"+idx, int(math.Round(34+float64(i)+1.5*d.wave(t, 7200, uint64(50+i)))))
		m.int(s+"2.1.1.13"+idx, health)
	}
	for i, rs := range d.nas.raidStatus {
		idx := "." + strconv.Itoa(i)
		m.int(s+"3.1.1.1"+idx, i)
		m.str(s+"3.1.1.2"+idx, nasRaids[i])
		m.int(s+"3.1.1.3"+idx, rs)
	}
}
