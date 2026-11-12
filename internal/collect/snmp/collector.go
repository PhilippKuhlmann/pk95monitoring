package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

// Profiles choose the MIBs read besides SNMPv2-MIB and IF-MIB.
const (
	ProfileAuto    = "auto"
	ProfileGeneric = "generic"
	ProfileSwitch  = "switch"
	ProfileUPS     = "ups"
	ProfilePrinter = "printer"
	ProfileNAS     = "nas"
	// ProfileUniFi is a UniFi (or EdgeSwitch) switch: the switch MIBs plus the Broadcom
	// FASTPATH vendor tree (model, CPU, memory, temperatures, fans, power supplies).
	ProfileUniFi = "unifi"
)

func normProfile(p string) string {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case ProfileGeneric, ProfileSwitch, ProfileUPS, ProfilePrinter, ProfileNAS, ProfileUniFi:
		return p
	}
	return ProfileAuto
}

// switchLike reports whether a profile reads the switch MIBs (LLDP, POWER-ETHERNET-MIB)
// and hides idle virtual interfaces.
func switchLike(profile string) bool { return profile == ProfileSwitch || profile == ProfileUniFi }

// SNMPv2-MIB system group and the objects used to detect the profile.
const (
	oidSysDescr       = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID    = "1.3.6.1.2.1.1.2.0"
	oidSysUpTime      = "1.3.6.1.2.1.1.3.0"
	oidSysContact     = "1.3.6.1.2.1.1.4.0"
	oidSysName        = "1.3.6.1.2.1.1.5.0"
	oidSysLocation    = "1.3.6.1.2.1.1.6.0"
	oidHrSystemUptime = "1.3.6.1.2.1.25.1.1.0"
	oidUpsIdentManuf  = "1.3.6.1.2.1.33.1.1.1.0"
	oidSynoModel      = "1.3.6.1.4.1.6574.1.5.1.0"
	oidPrtGeneral     = "1.3.6.1.2.1.43.5.1.1.1" // prtGeneralConfigChanges column
	enterprisesPrefix = "1.3.6.1.4.1."
)

// stateTTL drops the counter state of devices and interfaces not seen for this long.
const stateTTL = 6 * time.Hour

// Collector polls SNMP devices. It keeps the previous counter values per device to
// compute rates, so one Collector must be used for all polls of a device.
// The zero value is usable (real Dial, DefaultTimeout).
type Collector struct {
	Dial    DialFunc
	Timeout time.Duration    // per request; 0 = DefaultTimeout
	Now     func() time.Time // clock for rates; nil = time.Now

	mu    sync.Mutex
	state map[string]*devState // by device ID
}

// devState is what a device needs from the previous poll.
type devState struct {
	seen    time.Time
	uptime  float64
	ctr     map[string]sample
	watch   map[string]string // lower-case configured interface → ifName it resolved to
	profile string            // last reliably detected profile ("auto" only)
	static  staticCache       // slowly changing data (LLDP, bridge ports, inventory)
}

type sample struct {
	v    uint64
	bits int
	t    time.Time
}

// New returns a collector using the real gosnmp Dial.
func New() *Collector { return &Collector{Dial: Dial, Timeout: DefaultTimeout} }

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) dial(ctx context.Context, host string, cfg model.SNMPConfig) (Session, error) {
	dial := c.Dial
	if dial == nil {
		dial = Dial
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return dial(ctx, host, cfg, timeout)
}

// sysInfo is the system group of a device.
type sysInfo struct {
	name, descr, location, contact, objectID string
	uptime                                   float64 // seconds
	hasUptime                                bool
	upsMIB, synology                         bool
}

// readSystem reads the system group. It fails only when the device does not answer or
// neither sysDescr nor sysUpTime can be read.
func readSystem(s Session, host string) (sysInfo, error) {
	v, err := get(s, oidSysDescr, oidSysObjectID, oidSysUpTime, oidSysContact, oidSysName,
		oidSysLocation, oidHrSystemUptime, oidUpsIdentManuf, oidSynoModel)
	if err != nil {
		return sysInfo{}, fmt.Errorf("SNMP: keine Antwort von %s: %w", host, err)
	}
	_, hasDescr := v[oidSysDescr]
	ticks, hasUp := v.num(oidSysUpTime)
	if !hasDescr && !hasUp {
		return sysInfo{}, fmt.Errorf("SNMP: %s liefert keine Systemdaten (SNMPv2-MIB)", host)
	}
	si := sysInfo{
		name:     v.str(oidSysName),
		descr:    v.str(oidSysDescr),
		location: v.str(oidSysLocation),
		contact:  v.str(oidSysContact),
		objectID: v.str(oidSysObjectID),
		upsMIB:   v.str(oidUpsIdentManuf) != "",
		synology: v.str(oidSynoModel) != "",
	}
	// hrSystemUptime is the uptime of the host, sysUpTime only that of the agent.
	if hr, ok := v.num(oidHrSystemUptime); ok && hr > 0 {
		ticks, hasUp = hr, true
	}
	if hasUp {
		si.uptime, si.hasUptime = ticks/100, true
	}
	return si, nil
}

// enterprise returns the private enterprise number of a sysObjectID (318 = APC).
func enterprise(objectID string) int {
	rest, ok := strings.CutPrefix(trimDot(objectID), enterprisesPrefix)
	if !ok {
		return 0
	}
	num, _, _ := strings.Cut(rest, ".")
	n, _ := strconv.Atoi(num)
	return n
}

// detectProfile guesses the profile of a device: vendor by sysObjectID (and UniFi by
// sysDescr), then the presence of UPS-MIB or Printer-MIB, then the number of Ethernet ports. sure is false
// when the guess rests on a failed request (Printer-MIB walk or interface table).
func detectProfile(s Session, si sysInfo, ifs []iface, ifErr error) (profile string, sure bool) {
	switch enterprise(si.objectID) {
	case 318:
		return ProfileUPS, true
	case 6574:
		return ProfileNAS, true
	}
	uni, uniOK := isUniFi(s, si)
	if uni {
		return ProfileUniFi, true
	}
	if si.synology {
		return ProfileNAS, true
	}
	if si.upsMIB {
		return ProfileUPS, true
	}
	pdus, err := s.Walk(oidPrtGeneral)
	if err == nil && slices.ContainsFunc(pdus, func(p gosnmp.SnmpPDU) bool { return !missing(p) }) {
		return ProfilePrinter, true
	}
	eth := 0
	for _, f := range ifs {
		if isEthernet(f.typ) {
			eth++
		}
	}
	if eth > 4 {
		return ProfileSwitch, err == nil && uniOK
	}
	return ProfileGeneric, err == nil && ifErr == nil && uniOK
}

// autoProfile detects the profile of a device. A guess based on a failed request is
// replaced by the last reliable one, so a single timeout does not switch a printer to
// "generic" (which would drop its checks and silently close their alerts).
func (c *Collector) autoProfile(key string, s Session, si sysInfo, ifs []iface, ifErr error) string {
	profile, sure := detectProfile(s, si, ifs, ifErr)
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state[key]
	switch {
	case st == nil:
	case sure:
		st.profile = profile
	case st.profile != "":
		profile = st.profile
	}
	return profile
}

// guard stops asking a device that stopped answering in the middle of a poll: after a
// timeout that a small probe (sysUpTime) confirms, every further request fails at once
// instead of waiting for its own timeout and retry (a dozen walks would otherwise take
// minutes). A walk that times out while the device still answers small requests (large
// bulk responses dropped on the way) only fails that walk.
type guard struct {
	Session
	err error
}

func (g *guard) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	if g.err != nil {
		return nil, g.err
	}
	pdus, err := g.Session.Get(oids)
	g.check(err)
	return pdus, err
}

func (g *guard) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	if g.err != nil {
		return nil, g.err
	}
	pdus, err := g.Session.Walk(root)
	g.check(err)
	return pdus, err
}

func (g *guard) check(err error) {
	if err == nil || !isTimeout(err) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		g.err = err
		return
	}
	if _, perr := g.Session.Get([]string{oidSysUpTime}); perr != nil {
		g.err = err
	}
}

// isTimeout reports whether err means the device (or the poll) ran out of time.
func isTimeout(err error) bool {
	if errors.Is(err, gosnmp.ErrMaxRetriesExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}

// poll collects the checks of one device.
type poll struct {
	c       *Collector
	s       Session
	key     string // state key (device ID)
	host    string
	cfg     model.SNMPConfig
	now     time.Time
	sys     sysInfo
	profile string

	facts    map[string]string     // facts of snmp:system
	ports    map[string]*portExtra // LLDP/PoE data by ifIndex (switch-like profiles)
	sysState string
	sysMsgs  []string
	checks   []model.Check
}

func (p *poll) add(ck model.Check) { p.checks = append(p.checks, ck) }

// replace adds a check or replaces the one with the same key.
func (p *poll) replace(ck model.Check) {
	for i := range p.checks {
		if p.checks[i].Key == ck.Key {
			p.checks[i] = ck
			return
		}
	}
	p.add(ck)
}

// has reports whether a check with the key was produced already.
func (p *poll) has(key string) bool {
	return slices.ContainsFunc(p.checks, func(ck model.Check) bool { return ck.Key == key })
}

// optional handles the error of a walk or GET of an optional MIB: a timeout makes
// snmp:system unknown, any other error just means the MIB is not available.
func (p *poll) optional(what string, err error) {
	if err != nil && isTimeout(err) {
		p.partial(what, err)
	}
}

// problem raises the state of snmp:system with a message.
func (p *poll) problem(state, msg string) {
	p.sysState = model.Worst(p.sysState, state)
	p.sysMsgs = append(p.sysMsgs, msg)
}

// partial records a failed part of the poll (the device answered the system group, but a
// later walk failed): snmp:system becomes unknown.
func (p *poll) partial(what string, err error) {
	p.problem(model.StateUnknown, what+": "+err.Error())
}

// Collect polls a device and returns its checks. The error is set only when the device
// does not answer at all; missing MIBs just produce fewer checks.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	cfg := model.SNMPConfig{}
	if d.SNMP != nil {
		cfg = *d.SNMP
	}
	cfg = WithDefaults(cfg)
	conn, err := c.dial(ctx, d.Host, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	s := &guard{Session: conn}

	si, err := readSystem(s, d.Host)
	if err != nil {
		return nil, err
	}
	key := d.ID
	if key == "" {
		key = d.Host
	}
	p := &poll{c: c, s: s, key: key, host: d.Host, cfg: cfg, now: c.now(), sys: si,
		facts: map[string]string{}, sysState: model.StateOK}
	c.begin(key, si, p.now)

	ifs, ifErr := readInterfaces(s)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.profile = cfg.Profile
	if p.profile == ProfileAuto {
		p.profile = c.autoProfile(key, s, si, ifs, ifErr)
	}
	if switchLike(p.profile) {
		p.switchMIBs(ifs, ifErr)
	}
	p.interfaces(ifs, ifErr)
	switch p.profile {
	case ProfileUniFi:
		p.hostResources()
		p.unifi()
	case ProfileUPS:
		p.ups()
	case ProfilePrinter:
		p.printer()
	case ProfileNAS:
		p.hostResources()
		p.nas()
	default:
		p.hostResources()
	}
	p.custom()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]model.Check{p.systemCheck()}, p.checks...), nil
}

// begin prepares the counter state of a device: a reboot (uptime went back) invalidates
// all counters, and devices or counters not seen for stateTTL are dropped.
func (c *Collector) begin(key string, si sysInfo, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		c.state = map[string]*devState{}
	}
	for k, st := range c.state {
		if now.Sub(st.seen) > stateTTL {
			delete(c.state, k)
		}
	}
	st := c.state[key]
	if st == nil {
		st = &devState{ctr: map[string]sample{}}
		c.state[key] = st
	}
	if si.hasUptime {
		if si.uptime+1 < st.uptime {
			clear(st.ctr)
			st.static = staticCache{}
		}
		st.uptime = si.uptime
	}
	for k, smp := range st.ctr {
		if now.Sub(smp.t) > stateTTL {
			delete(st.ctr, k)
		}
	}
	st.seen = now
}

func (p *poll) systemCheck() model.Check {
	si := p.sys
	facts := map[string]string{}
	for k, v := range map[string]string{
		"Name": si.name, "Beschreibung": si.descr, "Standort": si.location, "Kontakt": si.contact,
	} {
		if v != "" {
			facts[k] = v
		}
	}
	for k, v := range p.facts {
		if v != "" {
			facts[k] = v
		}
	}
	facts["Profil"] = profileNames[p.profile]
	ck := model.Check{Key: "snmp:system", Name: "System", Group: "System", State: p.sysState, Facts: facts}
	if si.hasUptime {
		ck.Metrics = []model.Metric{{Name: "uptime", Value: si.uptime, Unit: "s"}}
		facts["Laufzeit"] = fmtUptime(si.uptime)
	}
	switch {
	case len(p.sysMsgs) > 0:
		ck.Message = strings.Join(p.sysMsgs, "; ")
	case si.hasUptime:
		ck.Message = "Läuft seit " + fmtUptime(si.uptime)
	default:
		ck.Message = "Erreichbar"
	}
	return ck
}

var profileNames = map[string]string{
	ProfileGeneric: "Allgemein",
	ProfileSwitch:  "Switch",
	ProfileUPS:     "USV",
	ProfilePrinter: "Drucker",
	ProfileNAS:     "NAS",
	ProfileUniFi:   "UniFi-Switch",
}

// custom reads the configured extra OIDs (check snmp:custom:<name>, metric "custom").
func (p *poll) custom() {
	oids := p.cfg.OIDs
	if len(oids) == 0 {
		return
	}
	var req []string
	for _, o := range oids {
		if validOID(o.OID) {
			req = append(req, trimDot(o.OID))
		}
	}
	var v vals
	var err error
	if len(req) > 0 {
		v, err = get(p.s, req...)
	}
	seen := map[string]bool{}
	for _, o := range oids {
		name := strings.TrimSpace(o.Name)
		if name == "" {
			name = trimDot(o.OID)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		ck := model.Check{Key: "snmp:custom:" + name, Name: name, Group: "Eigene Werte",
			State: model.StateUnknown, Facts: map[string]string{"OID": trimDot(o.OID)}}
		pdu, ok := v[trimDot(o.OID)]
		switch {
		case !validOID(o.OID):
			ck.Message = "Ungültige OID"
		case err != nil:
			ck.Message = "Abfrage fehlgeschlagen: " + err.Error()
		case !ok:
			ck.Message = "OID nicht vorhanden"
		default:
			if f, isNum := toFloat(pdu); isNum {
				scale := o.Scale
				if scale == 0 {
					scale = 1
				}
				f *= scale
				ck.State = model.StateOK
				ck.Message = strings.TrimSpace(fmtNum(f, 2) + " " + o.Unit)
				ck.Metrics = []model.Metric{{Name: "custom", Instance: name, Value: f, Unit: o.Unit}}
			} else {
				ck.State = model.StateOK
				ck.Message = toString(pdu)
			}
		}
		p.add(ck)
	}
}

// IfInfo describes one interface for the device dialog ("Test").
type IfInfo struct {
	Name  string `json:"name"`
	Descr string `json:"descr"`
	Alias string `json:"alias"`
	Speed uint64 `json:"speed"` // bit/s
	Oper  string `json:"oper"`  // up, down, testing, unknown, dormant, notPresent, lowerLayerDown
}

// TestResult is the answer of a connection test.
type TestResult struct {
	SysName    string   `json:"sys_name"`
	SysDescr   string   `json:"sys_descr"`
	Profile    string   `json:"profile"`
	Interfaces []IfInfo `json:"interfaces"`
}

// Test connects to a device and returns its identity, the detected (or configured)
// profile and its interfaces (without loopbacks).
func (c *Collector) Test(ctx context.Context, host string, cfg model.SNMPConfig) (TestResult, error) {
	cfg = WithDefaults(cfg)
	conn, err := c.dial(ctx, host, cfg)
	if err != nil {
		return TestResult{}, err
	}
	defer conn.Close()
	s := &guard{Session: conn}
	si, err := readSystem(s, host)
	if err != nil {
		return TestResult{}, err
	}
	ifs, ifErr := readInterfaces(s) // the identity alone is a successful test
	res := TestResult{SysName: si.name, SysDescr: si.descr, Profile: cfg.Profile, Interfaces: []IfInfo{}}
	if res.Profile == ProfileAuto {
		res.Profile, _ = detectProfile(s, si, ifs, ifErr)
	}
	for _, f := range ifs {
		if f.typ == ifTypeLoopback {
			continue
		}
		res.Interfaces = append(res.Interfaces, IfInfo{Name: f.name, Descr: f.descr, Alias: f.alias,
			Speed: f.speed, Oper: operName(f.oper)})
	}
	return res, ctx.Err()
}
