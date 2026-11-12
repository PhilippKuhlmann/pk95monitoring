package fakesnmp

import (
	"fmt"
	"math"
	"strconv"
)

// switchState holds the switch MIBs of a device: BRIDGE-MIB base ports, LLDP neighbors,
// PoE (POWER-ETHERNET-MIB plus the FASTPATH vendor table) and the FASTPATH fans.
type switchState struct {
	bridgePorts int   // dot1dBasePort 1…n map to ifIndex 1…n (0 = no BRIDGE-MIB)
	bridgeExtra []int // further base ports with ifIndex = base port (link aggregates)
	locPortBase int   // lldpLocPortNum = locPortBase + ifIndex
	locByName   bool  // lldpLocPortId is the ifName (else "Port <n>", as FASTPATH)
	neighbors   []lldpNeighbor

	poe       map[int]*poeSim // by PSE port number (= ifIndex), nil = no PoE
	poeBudget float64         // W
	fanFailed map[int]bool    // by fan number (1…)
}

// lldpNeighbor is one row of lldpRemTable.
type lldpNeighbor struct {
	ifIndex, timeMark int
	chassis           []byte // 6 bytes, macAddress subtype
	sysName           string
	portID            string // text (portSub 5 interfaceName, 7 local) …
	portMAC           []byte // … or 6 bytes (portSub 3 macAddress)
	portSub           int
	portDesc          string
}

// poeSim is the PoE state of one port: watts is the draw of the powered device (0 = none).
type poeSim struct {
	watts           float64
	class           int // 0…4
	disabled, fault bool
}

// mac returns a deterministic locally administered MAC address.
func (d *Device) mac(salt uint64) []byte {
	r := d.rnd(salt * 104729)
	return []byte{0x02, byte(r >> 8), byte(r >> 16), byte(r >> 24), byte(r >> 32), byte(r >> 40)}
}

// buildSwitchMIBs adds BRIDGE-MIB, LLDP-MIB and PoE objects; the caller holds d.mu.
func (d *Device) buildSwitchMIBs(t float64, m mib) {
	s := d.sw
	const base = "1.3.6.1.2.1.17.1.4.1.2."
	for i := 1; i <= s.bridgePorts; i++ {
		m.int(base+strconv.Itoa(i), i)
	}
	for _, i := range s.bridgeExtra {
		m.int(base+strconv.Itoa(i), i)
	}

	const loc, rem = "1.0.8802.1.1.2.1.3.7.1.", "1.0.8802.1.1.2.1.4.1.1."
	for _, p := range d.ports {
		if !isEthernet(p.typ) {
			continue
		}
		lp := "." + strconv.Itoa(s.locPortBase+p.index)
		if s.locByName {
			m.int(loc+"2"+lp, 5) // interfaceName
			m.str(loc+"3"+lp, p.name)
			m.str(loc+"4"+lp, p.descr)
		} else {
			m.int(loc+"2"+lp, 7) // local
			m.str(loc+"3"+lp, "Port "+strconv.Itoa(p.index))
			m.str(loc+"4"+lp, "")
		}
	}
	for i, n := range s.neighbors {
		if !d.portUp(n.ifIndex) {
			continue
		}
		idx := fmt.Sprintf(".%d.%d.%d", n.timeMark, s.locPortBase+n.ifIndex, i+1)
		m.int(rem+"4"+idx, 4) // macAddress
		m.bytes(rem+"5"+idx, n.chassis)
		m.int(rem+"6"+idx, n.portSub)
		if n.portMAC != nil {
			m.bytes(rem+"7"+idx, n.portMAC)
		} else {
			m.str(rem+"7"+idx, n.portID)
		}
		m.str(rem+"8"+idx, n.portDesc)
		m.str(rem+"9"+idx, n.sysName)
		m.str(rem+"10"+idx, "")
	}

	if s.poe != nil {
		d.buildPoE(t, m)
	}
}

func isEthernet(typ int) bool { return typ == 6 || typ == 117 }

// portUp reports whether the interface has link; the caller holds d.mu.
func (d *Device) portUp(index int) bool {
	for _, p := range d.ports {
		if p.index == index {
			return p.up
		}
	}
	return false
}

// buildPoE adds pethMainPseTable, pethPsePortTable (group 1) and the FASTPATH
// agentPethPsePortTable; the caller holds d.mu.
func (d *Device) buildPoE(t float64, m mib) {
	const port, main = "1.3.6.1.2.1.105.1.1.1.", "1.3.6.1.2.1.105.1.3.1.1."
	const vendor = "1.3.6.1.4.1.4413.1.1.15.1.1.1."
	total := 0.0
	for n := 1; n <= 64; n++ {
		ps := d.sw.poe[n]
		if ps == nil {
			continue
		}
		idx := ".1." + strconv.Itoa(n)
		admin, detect, class, fault := 1, 2, 1, 0 // searching, class0
		mw := 0.0
		switch {
		case ps.disabled:
			admin, detect = 2, 1
		case ps.fault:
			detect, fault = 4, 3 // fault, overload
		case ps.watts > 0 && d.portUp(n):
			detect, class = 3, ps.class+1
			mw = math.Round(ps.watts * (1 + 0.04*d.wave(t, 1800, uint64(300+n)) + 0.01*d.noise(t, uint64(400+n))) * 1000)
		}
		total += mw
		m.int(port+"1"+idx, 1)
		m.int(port+"2"+idx, n)
		m.int(port+"3"+idx, admin)
		m.int(port+"6"+idx, detect)
		m.int(port+"7"+idx, 3) // priority low
		m.int(port+"10"+idx, class)
		m.c32(port+"12"+idx, 0)
		limit := 30000
		if n > 16 {
			limit = 60000 // PoE++ ports
		}
		m.gauge(vendor+"1"+idx, float64(limit))
		m.gauge(vendor+"2"+idx, mw)
		m.gauge(vendor+"3"+idx, math.Round(mw/53))
		m.gauge(vendor+"4"+idx, 53)
		m.int(vendor+"9"+idx, fault)
	}
	m.gauge(main+"2.1", d.sw.poeBudget)
	m.int(main+"3.1", 1)
	m.gauge(main+"4.1", math.Round(total/1000))
	m.int(main+"5.1", 90)
}

// SetPoE switches PoE on the port with the given ifIndex on or off (UniFi switch).
func (d *Device) SetPoE(index int, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ps := d.sw.poe[index]; ps != nil {
		ps.disabled = !enabled
	}
}

// SetPoEFault sets an overload fault on the PoE port with the given ifIndex.
func (d *Device) SetPoEFault(index int, fault bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ps := d.sw.poe[index]; ps != nil {
		ps.fault = fault
	}
}

// SetFanFailed marks fan n (1…) of a UniFi switch as failed.
func (d *Device) SetFanFailed(n int, failed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sw.fanFailed == nil {
		d.sw.fanFailed = map[int]bool{}
	}
	d.sw.fanFailed[n] = failed
}

// NewUniFiSwitch returns a UniFi USW-Pro-24-PoE (Broadcom FASTPATH, sysObjectID 4413):
// 24 gigabit PoE ports (ifIndex 1…24, ifName "0/<n>"; 1–16 PoE+, 17–24 PoE++) and two
// SFP+ ports (25, 26), the "CPU Interface" (65) and two idle link aggregates (66, 67).
// Ports 20–23 have no link. Twelve ports power devices (access points, cameras, phones,
// a door controller; about 110 W of a 400 W budget). LLDP neighbors sit on the access
// points (1–3), a camera without system name (4), pve1 (13), the gateway (24) and the
// uplink (25); lldpRemLocalPortNum is the bridge port (= ifIndex). The vendor tree has
// inventory, the CPU string, memory, two temperature sensors, two fans and one power supply.
func NewUniFiSwitch() *Device {
	d := newDevice("unifi-usw-pro-24-poe")
	const fw = "7.0.50.15613"
	sys := sysDesc{
		descr:    "USW-Pro-24-PoE, " + fw + ", Linux 4.19.152",
		objectID: "1.3.6.1.4.1.4413", name: "usw-pro-24-poe", location: "Technikraum", contact: "it@example.com",
	}
	names := map[int]string{
		1: "AP Büro EG", 2: "AP Büro OG", 3: "AP Lager", 4: "Kamera Einfahrt", 5: "Kamera Hof",
		6: "Kamera Lager", 7: "Telefon Empfang", 8: "Telefon Büro 1", 9: "Telefon Büro 2",
		10: "Telefon Chef", 11: "Zutritt Tür", 12: "Drucker", 13: "pve1", 14: "pve2", 15: "NAS",
		16: "AP Garten", 24: "Gateway",
	}
	type pd struct {
		watts float64
		class int
	}
	pds := map[int]pd{
		1: {11.5, 4}, 2: {10.8, 4}, 3: {21.0, 4}, 4: {4.6, 0}, 5: {4.9, 3}, 6: {5.2, 3},
		7: {3.1, 2}, 8: {3.3, 2}, 9: {2.9, 2}, 10: {6.2, 2}, 11: {7.4, 3}, 16: {13.5, 4},
	}
	d.sw.poe = map[int]*poeSim{}
	for i := 1; i <= 24; i++ {
		name := names[i]
		if name == "" {
			name = "Port " + strconv.Itoa(i)
		}
		avg := 20e3 + float64(d.rnd(uint64(i))%400)*1e3
		switch {
		case i <= 3 || i == 16:
			avg = 2e6
		case i >= 13 && i <= 15:
			avg = 6e6
		case i == 24:
			avg = 9e6
		}
		d.addPort(i, "0/"+strconv.Itoa(i), fmt.Sprintf("Slot: 0 Port: %d Gigabit - Level", i), name, 6, 1e9,
			i < 20 || i > 23, avg)
		d.sw.poe[i] = &poeSim{watts: pds[i].watts, class: pds[i].class}
	}
	d.addPort(25, "0/25", "Slot: 0 Port: 25 10G - Level", "Uplink Kern-Switch", 6, 10e9, true, 20e6)
	d.addPort(26, "0/26", "Slot: 0 Port: 26 10G - Level", "Server-Switch", 6, 10e9, true, 8e6)
	d.addPort(65, "5/1", "CPU Interface for Slot: 5 Port: 1", "", 1, 0, true, 5e3)
	d.addPort(66, "3/1", "Link Aggregate 1", "", 1, 0, false, 0)
	d.addPort(67, "3/2", "Link Aggregate 2", "", 1, 0, false, 0)
	d.sw.poeBudget = 400
	d.sw.bridgePorts = 26
	d.sw.bridgeExtra = []int{66, 67}
	d.sw.neighbors = []lldpNeighbor{
		{ifIndex: 1, timeMark: 72, sysName: "AP-Buero-EG", portID: "eth0", portSub: 5},
		{ifIndex: 2, timeMark: 72, sysName: "AP-Buero-OG", portID: "eth0", portSub: 5},
		{ifIndex: 3, timeMark: 72, sysName: "AP-Lager", portID: "eth0", portSub: 5},
		{ifIndex: 4, timeMark: 0, portSub: 3}, // camera: chassis and port id only
		{ifIndex: 13, timeMark: 0, sysName: "pve1", portSub: 3, portDesc: "eno1"},
		{ifIndex: 24, timeMark: 0, sysName: "UDM-Pro", portID: "Port 9", portSub: 7},
		{ifIndex: 25, timeMark: 0, sysName: "sw-core", portID: "Te1/1/1", portSub: 5, portDesc: "TenGigabitEthernet1/1/1"},
	}
	for i := range d.sw.neighbors {
		n := &d.sw.neighbors[i]
		n.chassis = d.mac(uint64(200 + i))
		if n.portSub == 3 {
			n.portMAC = d.mac(uint64(300 + i))
		}
	}
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildSwitchMIBs(t, m)
		d.buildFastpath(t, m, sys.descr, fw)
	}
	return d
}

// buildFastpath adds the Broadcom FASTPATH tree (inventory, CPU, memory, boxServices) and
// the chassis row of ENTITY-MIB; the caller holds d.mu.
func (d *Device) buildFastpath(t float64, m mib, descr, fw string) {
	const inv, sw, box = "1.3.6.1.4.1.4413.1.1.1.1.1.", "1.3.6.1.4.1.4413.1.1.1.1.4.", "1.3.6.1.4.1.4413.1.1.43.1."
	serial := fmt.Sprintf("%x", d.mac(1))
	m.str(inv+"1.0", descr)
	m.str(inv+"2.0", "USW-Pro-24-PoE")
	m.str(inv+"3.0", "USPM24P")
	m.str(inv+"4.0", serial)
	m.str(inv+"6.0", "0")
	m.str(inv+"8.0", "Ubiquiti Inc.")
	m.bytes(inv+"9.0", d.mac(1))
	m.str(inv+"10.0", "Linux 4.19.152")
	m.str(inv+"13.0", fw)
	m.str(inv+"14.0", "A")

	cpu := func(period float64, salt uint64) float64 {
		return math.Max(1, 9+4*d.wave(t, period, salt)+1.5*d.noise(t, salt+1))
	}
	m.str(sw+"9.0", fmt.Sprintf("    5 Secs ( %.4f%%)   60 Secs ( %.4f%%)  300 Secs ( %.4f%%)",
		cpu(600, 500), cpu(1800, 502), cpu(3600, 504)))
	const totalKB = 1015808.0
	used := 0.58 + 0.04*d.wave(t, 5400, 510)
	m.int(sw+"1.0", int(totalKB*(1-used)))
	m.int(sw+"2.0", int(totalKB))

	m.int(box+"1.0", -5) // normal temperature range
	m.int(box+"2.0", 85)
	temps := []float64{47 + 3*d.wave(t, 7200, 520) + 0.5*d.noise(t, 521), 55 + 4*d.wave(t, 7200, 522) + 0.5*d.noise(t, 523)}
	hottest := 0.0
	for i, tc := range temps {
		idx := ".1." + strconv.Itoa(i)
		m.gauge(box+"8.1.2"+idx, float64(i))
		m.int(box+"8.1.3"+idx, 1) // fixed
		m.int(box+"8.1.5"+idx, int(math.Round(tc)))
		hottest = math.Max(hottest, math.Round(tc))
	}
	m.int(box+"15.1.1.1", 1)
	m.int(box+"15.1.2.1", 1) // normal
	m.int(box+"15.1.3.1", int(hottest))
	for i := range 2 {
		idx := ".1." + strconv.Itoa(i)
		state, rpm := 2, 3600+400*d.wave(t, 3600, uint64(530+i))+40*d.noise(t, uint64(540+i))
		if d.sw.fanFailed[i+1] {
			state, rpm = 3, 0
		}
		m.int(box+"6.1.1"+idx, i)
		m.int(box+"6.1.2"+idx, 2) // removable
		m.int(box+"6.1.3"+idx, state)
		m.str(box+"6.1.4"+idx, strconv.Itoa(int(math.Round(rpm)))) // OCTET STRING in the EdgeSwitch MIB
		m.str(box+"6.1.5"+idx, strconv.Itoa(int(math.Round(rpm/60))))
		m.gauge(box+"6.1.6"+idx, 1)
	}
	m.int(box+"7.1.1.1.0", 0)
	m.int(box+"7.1.2.1.0", 1) // fixed
	m.int(box+"7.1.3.1.0", 2) // operational

	const ent = "1.3.6.1.2.1.47.1.1.1.1."
	m.str(ent+"2.1", "USW-Pro-24-PoE")
	m.int(ent+"5.1", 3) // chassis
	m.str(ent+"10.1", fw)
	m.str(ent+"11.1", serial)
	m.str(ent+"13.1", "USW-Pro-24-PoE")
}
