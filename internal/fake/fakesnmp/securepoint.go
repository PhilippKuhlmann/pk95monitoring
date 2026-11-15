package fakesnmp

import (
	"math"
	"strconv"
	"time"

	"github.com/gosnmp/gosnmp"
)

// securepointState is the changeable state of a simulated Securepoint UTM.
type securepointState struct {
	tunnels     []spTunnel
	subnetsDown map[int]bool
	peers       []spPeer
	licenseDays int    // comLicDaysLeft; 0xffffffff = unlimited
	syncState   string // clusterStatusSyncState: synchronized, pending, error
	clusterRole string // master, backup
	appError    map[string]bool
	avFailed    bool
	avUnused    bool // no Ikarus license: ikarus error(4), no pattern ever loaded
	conntrack   int  // nf_conntrack_count
}

// spTime is the timestamp format of the UTM tables ("2026-11-15T15:15:48+0100").
const spTime = "2006-01-02T15:04:05-0700"

type spTunnel struct {
	name   string
	status int // 0 unknown, 1 up, 2 down
}

type spPeer struct {
	name, endpoint, key string
	handshakeAgo        float64 // seconds; < 0 = never
}

// NewSecurepoint returns a Securepoint UTM 14 (net-snmp, SECUREPOINT-MIB) in a cluster as
// master with two IPsec tunnels, WireGuard peers and a license ending in 120 days.
func NewSecurepoint() *Device {
	d := newDevice("securepoint")
	d.sp = securepointState{
		tunnels:     []spTunnel{{"Zentrale-Lager", 1}, {"Zentrale-Steuerbuero", 1}},
		subnetsDown: map[int]bool{},
		peers: []spPeer{
			{"Lager-Kasse", "91.40.12.7:51820", "dGhpcyBpcyBhIGRlbW8gd2lyZWd1YXJkIGtleSAxMjM=", 35},
			{"Notebook-Chef", "", "YW5vdGhlciBkZW1vIHdpcmVndWFyZCBrZXkgNDU2Nzg=", -1},
		},
		licenseDays: 120, syncState: "synchronized", clusterRole: "master",
		appError: map[string]bool{}, conntrack: 3800,
	}
	sys := sysDesc{
		descr:    "Linux utm-lager 6.1.112-securepoint #1 SMP x86_64",
		objectID: "1.3.6.1.4.1.8072.3.2.10", name: "utm-lager", location: "Lager", contact: "it@example.com",
	}
	d.addPort(1, "LAN1", "LAN1", "extern", 6, 1e9, true, 2.5e6)
	d.addPort(2, "LAN2", "LAN2", "intern", 6, 1e9, true, 3e6)
	d.addPort(3, "LAN3", "LAN3", "Cluster-Sync", 6, 1e9, true, 5e4)
	d.addPort(4, "wg0", "wg0", "", 65, 0, true, 2e5)
	d.build = func(t float64, m mib) {
		d.buildSystem(t, m, sys)
		d.buildPorts(t, m)
		d.buildSecurepoint(t, m)
	}
	return d
}

// SetSPTunnel sets an IPsec tunnel of the Securepoint UTM: 1 up, 2 down.
func (d *Device) SetSPTunnel(name string, status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.sp.tunnels {
		if d.sp.tunnels[i].name == name {
			d.sp.tunnels[i].status = status
		}
	}
}

// SetSPSubnetDown takes subnet pair n (0-based) down.
func (d *Device) SetSPSubnetDown(n int, down bool) {
	d.mu.Lock()
	d.sp.subnetsDown[n] = down
	d.mu.Unlock()
}

// SetSPHandshakeAgo sets the age of the last handshake of a WireGuard peer (seconds, < 0 never).
func (d *Device) SetSPHandshakeAgo(name string, sec float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.sp.peers {
		if d.sp.peers[i].name == name {
			d.sp.peers[i].handshakeAgo = sec
		}
	}
}

// SetSPLicenseDays sets comLicDaysLeft.
func (d *Device) SetSPLicenseDays(days int) { d.mu.Lock(); d.sp.licenseDays = days; d.mu.Unlock() }

// SetSPCluster sets the cluster role and sync state (synchronized, pending, error).
func (d *Device) SetSPCluster(role, sync string) {
	d.mu.Lock()
	d.sp.clusterRole, d.sp.syncState = role, sync
	d.mu.Unlock()
}

// SetSPAppError puts an application (named, squid, …) into error(4).
func (d *Device) SetSPAppError(name string, failed bool) {
	d.mu.Lock()
	d.sp.appError[name] = failed
	d.mu.Unlock()
}

// SetSPAVUnlicensed simulates a UTM without an Ikarus license: the ikarus service is in
// error(4) and its pattern table row has no update and no run (as on a real UTM 14.1).
func (d *Device) SetSPAVUnlicensed(v bool) { d.mu.Lock(); d.sp.avUnused = v; d.mu.Unlock() }

// SetSPAVFailed lets the virus pattern update fail.
func (d *Device) SetSPAVFailed(failed bool) { d.mu.Lock(); d.sp.avFailed = failed; d.mu.Unlock() }

// SetSPConntrack sets the number of tracked connections (the table holds 65536).
func (d *Device) SetSPConntrack(n int) { d.mu.Lock(); d.sp.conntrack = n; d.mu.Unlock() }

func (d *Device) buildSecurepoint(t float64, m mib) {
	// the UTM serves its scalars without the ".0" instance suffix
	const com = "1.3.6.1.4.1.28553.1."
	m.str(com+"1", "utm")
	for i, n := range []uint{14, 1, 9, 3} {
		m.put(com+"2."+strconv.Itoa(i+1), gosnmp.Gauge32, n)
	}
	m.str(com+"2.5", "(null)")
	m.str(com+"2.6", "none")
	m.str(com+"2.7", "14.1.9.3")
	m.str(com+"2.8", "none")
	lic := []any{"Muster GmbH", "it@example.com", "Software G4", 25, d.sp.licenseDays, uint(4294967295),
		"update-001.v12.utm.spnoc.de", "v11.cloudbackup.securepoint.de", "v11.licensing.securepoint.de", "UTMv11"}
	for i, v := range lic {
		oid := com + "3." + strconv.Itoa(i+1)
		switch x := v.(type) {
		case string:
			m.str(oid, x)
		case int:
			m.put(oid, gosnmp.Gauge32, uint(uint32(x)))
		case uint:
			m.put(oid, gosnmp.Gauge32, x)
		}
	}
	// net-snmp UCD: CPU idle, load, memory in kB
	m.int("1.3.6.1.4.1.2021.11.11.0", int(math.Round(88-8*d.wave(t, 1800, 90))))
	m.str("1.3.6.1.4.1.2021.10.1.3.1", strconv.FormatFloat(0.4+0.2*d.wave(t, 900, 91), 'f', 2, 64))
	m.int("1.3.6.1.4.1.2021.4.5.0", 8152340)
	m.int("1.3.6.1.4.1.2021.4.6.0", int(4100000+300000*d.wave(t, 3600, 92)))
	m.int("1.3.6.1.4.1.2021.4.14.0", 210000)
	m.int("1.3.6.1.4.1.2021.4.15.0", 1300000)

	const utm = "1.3.6.1.4.1.28553.2.1."
	// appStatusEntry (1.1.1): 1 running, 2 stopped, 4 error, 9 not configured
	for i, app := range []struct {
		name   string
		status int
	}{{"ikarus", 1}, {"ntpd", 1}, {"snmpd", 1}, {"http_proxy", 1}, {"openvpn", 2}, {"ipsec", 1},
		{"smtpd", 2}, {"named", 1}, {"dhcpd", 1}, {"webfilter", 1}, {"wap", 2}} {
		idx := "." + strconv.Itoa(i)
		st := app.status
		if d.sp.appError[app.name] || (app.name == "ikarus" && d.sp.avUnused) {
			st = 4
		}
		m.put(utm+"1.1.1.1"+idx, gosnmp.Gauge32, uint(i))
		m.str(utm+"1.1.1.2"+idx, app.name)
		m.int(utm+"1.1.1.3"+idx, st)
		m.put(utm+"1.1.1.4"+idx, gosnmp.Gauge32, uint(0))
	}
	for i, tu := range d.sp.tunnels {
		idx := "." + strconv.Itoa(i+1)
		m.int(utm+"2.1.1.1"+idx, i+1)
		m.str(utm+"2.1.1.2"+idx, tu.name)
		m.int(utm+"2.1.1.3"+idx, tu.status)
	}
	for i, sn := range []string{"10.20.0.0/24 ↔ 10.30.0.0/24", "10.20.0.0/24 ↔ 10.31.0.0/24", "10.20.0.0/24 ↔ 10.40.0.0/24"} {
		idx := "." + strconv.Itoa(i+1)
		st := "up"
		if d.sp.subnetsDown[i] {
			st = "down"
		}
		m.int(utm+"2.2.1.1"+idx, i+1)
		m.str(utm+"2.2.1.2"+idx, sn)
		m.str(utm+"2.2.1.6"+idx, st)
	}
	now := float64(d.clock().Unix())
	// avPatternStatusEntry (5.1.1): product, last update, last run (timestamps), state (fail = 2)
	avState, updated, ran := 1, d.clock().Add(-90*time.Minute).Format(spTime), d.clock().Add(-30*time.Minute).Format(spTime)
	if d.sp.avFailed {
		avState = 2
	}
	if d.sp.avUnused {
		avState, updated, ran = 2, "", ""
	}
	m.put(utm+"5.1.1.1.0", gosnmp.Gauge32, uint(0))
	m.str(utm+"5.1.1.2.0", "ikarus")
	m.str(utm+"5.1.1.3.0", updated)
	m.str(utm+"5.1.1.4.0", ran)
	m.int(utm+"5.1.1.5.0", avState)
	m.put(utm+"10.1", gosnmp.Gauge32, uint(65536))
	m.put(utm+"10.2", gosnmp.Gauge32, uint(d.sp.conntrack+int(400*d.wave(t, 600, 93))))
	for i, pe := range d.sp.peers {
		idx := "." + strconv.Itoa(i+1)
		hs := "0"
		if pe.handshakeAgo >= 0 {
			hs = strconv.FormatInt(int64(now-pe.handshakeAgo), 10)
		}
		m.int(utm+"11.2.1.1"+idx, i+1)
		m.str(utm+"11.2.1.2"+idx, pe.name)
		m.str(utm+"11.2.1.3"+idx, "1")
		m.str(utm+"11.2.1.4"+idx, pe.key)
		m.str(utm+"11.2.1.5"+idx, pe.endpoint)
		m.str(utm+"11.2.1.6"+idx, hs)
		m.c64(utm+"11.2.1.7"+idx, 8e8+t*3e3)
		m.c64(utm+"11.2.1.8"+idx, 2e8+t*1e3)
	}
	m.int(utm+"12.1", 1)
	m.str(utm+"12.2", "HIGH")
	m.str(utm+"12.3", d.sp.clusterRole)
	m.str(utm+"12.4", d.sp.syncState)
	m.str(utm+"12.5", "false")
}
