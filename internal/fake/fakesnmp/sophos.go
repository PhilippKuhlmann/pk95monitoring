package fakesnmp

import (
	"math"
	"strconv"
	"time"
)

// sophosState is the changeable state of a simulated Sophos Firewall.
type sophosState struct {
	haPeer   int // HaState of the peer: 1 auxiliary, 0 none, 4 faulty
	tunnels  []sophosTunnel
	licenses map[int]sophosLicense // by sfosXGLicenseDetails index
	services map[int]int           // ServiceStatsType overrides by index
}

type sophosTunnel struct {
	name, descr, policy string
	typ, status, active int
}

type sophosLicense struct {
	status int       // SubscriptionStatusType: 1 trial, 3 subscribed, 4 expired
	until  time.Time // zero: "Perpetual"
}

// NewSophos returns a Sophos XGS 2100 (SFOS 21) in an active-passive HA cluster with three
// IPsec tunnels and subscriptions; the Web Protection subscription ends in 20 days.
func NewSophos() *Device {
	d := newDevice("sophos")
	now := time.Now()
	d.fw = sophosState{
		haPeer: 1,
		tunnels: []sophosTunnel{
			{"Filiale Nord", "Site-to-Site Hamburg", "IKEv2_AES256_SHA256", 2, 1, 1},
			{"Rechenzentrum", "Backup-RZ Frankfurt", "IKEv2_AES256_SHA256", 2, 1, 1},
			{"Homeoffice GF", "Host-to-Host", "IKEv2_AES128_SHA1", 1, 0, 1},
		},
		licenses: map[int]sophosLicense{
			1: {3, time.Time{}},
			2: {3, now.AddDate(0, 7, 3)},
			3: {3, now.AddDate(0, 0, 20)},
			6: {3, now.AddDate(0, 7, 3)},
			7: {3, now.AddDate(0, 7, 3)},
		},
		services: map[int]int{},
	}
	sys := sysDesc{
		descr:    "Sophos Firewall XGS2100 SFOS 21.0.1 MR-1-Build272",
		objectID: "1.3.6.1.4.1.2604.5", name: "fw-zentrale", location: "Serverraum", contact: "it@example.com",
	}
	d.addPort(1, "Port1", "Port1", "LAN", 6, 1e9, true, 6e6)
	d.addPort(2, "Port2", "Port2", "WAN Telekom", 6, 1e9, true, 4e6)
	d.addPort(3, "Port3", "Port3", "DMZ", 6, 1e9, true, 4e5)
	d.addPort(4, "Port4", "Port4", "HA-Link", 6, 1e9, true, 2e4)
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		for i := 1; i <= 4; i++ {
			load := 18 + 12*d.wave(t, 1800, uint64(60+i)) + 4*d.noise(t, uint64(70+i))
			m.int("1.3.6.1.2.1.25.3.3.1.2."+strconv.Itoa(196607+i), int(math.Round(max(load, 0))))
		}
		d.buildSophos(t, m)
	}
	return d
}

// SetHAPeer sets the HA state of the peer: 1 auxiliary (normal), 0 none (gone), 4 faulty.
func (d *Device) SetHAPeer(state int) {
	d.mu.Lock()
	d.fw.haPeer = state
	d.mu.Unlock()
}

// SetTunnel sets the status of an IPsec tunnel: 0 inactive, 1 active, 2 partially active.
func (d *Device) SetTunnel(name string, status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.fw.tunnels {
		if d.fw.tunnels[i].name == name {
			d.fw.tunnels[i].status = status
		}
	}
}

// SetLicense sets a subscription (index of sfosXGLicenseDetails).
func (d *Device) SetLicense(n, status int, until time.Time) {
	d.mu.Lock()
	d.fw.licenses[n] = sophosLicense{status, until}
	d.mu.Unlock()
}

// SetService sets the status of a service (index of sfosXGServiceStatus, ServiceStatsType).
func (d *Device) SetService(n, status int) {
	d.mu.Lock()
	d.fw.services[n] = status
	d.mu.Unlock()
}

func (d *Device) buildSophos(t float64, m mib) {
	const s = "1.3.6.1.4.1.2604.5.1."
	m.str(s+"1.1.0", "fw-zentrale")
	m.str(s+"1.2.0", "XGS2100")
	m.str(s+"1.3.0", "SFOS 21.0.1 MR-1-Build272")
	m.str(s+"1.4.0", "X21009XNVYKG7A2")
	m.ticks(s+"2.2.0", d.baseUptime+t)
	m.gauge(s+"2.4.1.0", 120000)
	m.gauge(s+"2.4.2.0", math.Round(31+0.000001*t))
	m.gauge(s+"2.5.1.0", 8000)
	m.gauge(s+"2.5.2.0", math.Round(58+6*d.wave(t, 3600, 80)))
	m.gauge(s+"2.5.3.0", 4000)
	m.gauge(s+"2.5.4.0", 3)
	m.gauge(s+"2.6.0", math.Round(42+10*d.wave(t, 7200, 81)))
	// HA: this device is primary, the peer auxiliary
	m.int(s+"4.1.0", 1)
	m.str(s+"4.2.0", "X21009XNVYKG7A2")
	m.str(s+"4.3.0", "X21009XNVYKG7B9")
	m.int(s+"4.4.0", 3)
	m.int(s+"4.5.0", d.fw.haPeer)
	m.str(s+"4.6.0", "Active-Passive")
	m.put(s+"4.10.0", 0x40, "10.255.255.2") // IpAddress
	// subscriptions: status and expiry date ("Perpetual" without one)
	for n := 1; n <= 9; n++ {
		l, ok := d.fw.licenses[n]
		if !ok {
			m.int(s+"5."+strconv.Itoa(n)+".1.0", 2)
			m.str(s+"5."+strconv.Itoa(n)+".2.0", "")
			continue
		}
		m.int(s+"5."+strconv.Itoa(n)+".1.0", l.status)
		date := "Perpetual"
		if !l.until.IsZero() {
			date = l.until.Format("Jan 2 2006")
		}
		m.str(s+"5."+strconv.Itoa(n)+".2.0", date)
	}
	// services: everything used runs, mail services are not in use
	for n := 1; n <= 21; n++ {
		st := 3
		switch n {
		case 1, 2, 3, 4, 7:
			st = 1
		case 19:
			st = 0
		}
		if o, ok := d.fw.services[n]; ok {
			st = o
		}
		m.int(s+"3."+strconv.Itoa(n)+".0", st)
	}
	for i, tu := range d.fw.tunnels {
		idx := "." + strconv.Itoa(i+1)
		const e = s + "6.1.1.1.1."
		m.int(e+"1"+idx, i+1)
		m.str(e+"2"+idx, tu.name)
		m.str(e+"3"+idx, tu.descr)
		m.str(e+"4"+idx, tu.policy)
		m.str(e+"5"+idx, "Main Mode")
		m.int(e+"6"+idx, tu.typ)
		m.str(e+"7"+idx, "Port2")
		m.int(e+"8"+idx, 1)
		m.int(e+"9"+idx, tu.status)
		m.int(e+"10"+idx, tu.active)
	}
}
