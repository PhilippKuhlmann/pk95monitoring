package snmp

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

// LLDP-MIB, BRIDGE-MIB, POWER-ETHERNET-MIB (RFC 3621) and the Broadcom FASTPATH PoE table
// (agentPethPsePortTable, AUGMENTS pethPsePortEntry; UniFi and EdgeSwitch).
const (
	oidLldpRemEntry     = "1.0.8802.1.1.2.1.4.1.1"
	oidLldpLocPortEntry = "1.0.8802.1.1.2.1.3.7.1"
	oidDot1dBasePort    = "1.3.6.1.2.1.17.1.4.1" // dot1dBasePortEntry
	oidPethPsePortEntry = "1.3.6.1.2.1.105.1.1.1"
	oidPethMainPseEntry = "1.3.6.1.2.1.105.1.3.1.1"
	fpPethPortEntry     = ".15.1.1.1" // below the FASTPATH base

	dot1dBasePortIfIndex = 2

	lldpRemChassisSubtype, lldpRemChassisID, lldpRemPortSubtype = 4, 5, 6
	lldpRemPortID, lldpRemPortDesc, lldpRemSysName              = 7, 8, 9
	lldpLocPortID, lldpLocPortDesc                              = 3, 4
	lldpChassisMAC, lldpPortMAC                                 = 4, 3 // subtypes macAddress

	pethPortAdmin, pethPortDetection, pethPortClass  = 3, 6, 10
	pethMainPower, pethMainOper, pethMainConsumption = 2, 3, 4
	fpPethOutputPower, fpPethFault                   = 2, 9

	pethDetDisabled, pethDetSearching, pethDetDelivering = 1, 2, 3
	pethDetFault, pethDetTest, pethDetOtherFault         = 4, 5, 6

	// staticTTL is how long slowly changing data (LLDP neighbors, bridge ports, inventory,
	// FASTPATH base) is reused before it is read again.
	staticTTL    = 5 * time.Minute
	maxNeighbors = 3 // neighbor names listed per port
)

// staticCache holds data of a device that changes rarely, so it is not walked every poll.
// Maps are replaced as a whole, never modified after they are stored.
type staticCache struct {
	lldpAt   time.Time
	lldp     map[string]neighbor // by ifIndex
	basePort map[string]string   // dot1dBasePort → ifIndex

	fpAt   time.Time
	fpBase string // FASTPATH base OID; "" = the device has none

	entAt time.Time
	ent   entityInfo
}

func fresh(at, now time.Time) bool {
	return !at.IsZero() && !now.Before(at) && now.Sub(at) < staticTTL
}

// staticData returns a copy of the cached static data of the device.
func (p *poll) staticData() staticCache {
	p.c.mu.Lock()
	defer p.c.mu.Unlock()
	if st := p.c.state[p.key]; st != nil {
		return st.static
	}
	return staticCache{}
}

// setStatic changes the cached static data of the device.
func (p *poll) setStatic(fn func(*staticCache)) {
	p.c.mu.Lock()
	defer p.c.mu.Unlock()
	if st := p.c.state[p.key]; st != nil {
		fn(&st.static)
	}
}

// neighbor is what LLDP knows about the device on the other end of a port.
type neighbor struct{ name, port string }

// poePort is the PoE state of one port.
type poePort struct {
	admin, detect, class int     // 0 = not readable
	mw                   float64 // output power in mW (vendor table)
	hasPower             bool
	fault                int // vendor fault status (0 none, 1 mpsAbsent, 2 short …)
	hasFault             bool
}

// portExtra is LLDP and PoE data of one interface (by ifIndex).
type portExtra struct {
	neighbor neighbor
	poe      *poePort
}

func (p *poll) extra(ifIndex string) *portExtra {
	x := p.ports[ifIndex]
	if x == nil {
		x = &portExtra{}
		p.ports[ifIndex] = x
	}
	return x
}

// poeFaults are the German texts of agentPethFaultStatus (fact "PoE-Fehler").
var poeFaults = map[int]string{
	2: "Kurzschluss", 3: "Überlast", 4: "Leistung verweigert", 5: "Überhitzung", 6: "Startfehler",
}

// faulty reports whether the port has a PoE fault.
func (pp *poePort) faulty() bool {
	return pp.detect == pethDetFault || pp.detect == pethDetOtherFault ||
		(pp.hasFault && pp.fault != 0 && pp.fault != 1)
}

// delivering reports whether the port powers a device.
func (pp *poePort) delivering() bool {
	return !pp.faulty() && (pp.detect == pethDetDelivering || (pp.detect == 0 && pp.hasPower && pp.mw > 0))
}

// decorate adds the LLDP facts (Nachbar, Nachbar-Port) and the PoE facts (PoE,
// PoE-Klasse, PoE-Fehler) and metric (poe.port in W) to the check of interface f.
func (x *portExtra) decorate(ck *model.Check, f iface) {
	if ck.Facts == nil {
		ck.Facts = map[string]string{}
	}
	if x.neighbor.name != "" {
		ck.Facts["Nachbar"] = x.neighbor.name
	}
	if x.neighbor.port != "" {
		ck.Facts["Nachbar-Port"] = x.neighbor.port
	}
	pp := x.poe
	if pp == nil {
		return
	}
	switch {
	case pp.faulty():
		ck.Facts["PoE"] = "Fehler"
		if msg, ok := poeFaults[pp.fault]; ok && pp.hasFault {
			ck.Facts["PoE-Fehler"] = msg
		}
	case pp.delivering():
		ck.Facts["PoE"] = "aktiv"
		if pp.hasPower {
			w := pp.mw / 1000
			ck.Facts["PoE"] = "aktiv · " + fmtNum(w, 1) + " W"
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "poe.port", Instance: f.name, Value: w, Unit: "W"})
		}
		if pp.class >= 1 && pp.class <= 5 {
			ck.Facts["PoE-Klasse"] = strconv.Itoa(pp.class - 1)
		}
	case pp.detect == pethDetTest:
		ck.Facts["PoE"] = "Test"
	case pp.admin == 2 || pp.detect == pethDetDisabled || pp.detect == pethDetSearching:
		ck.Facts["PoE"] = "aus"
	}
}

// switchMIBs reads LLDP neighbors (cached for staticTTL) and PoE into p.ports and adds
// snmp:poe. Every table is optional: a device without them just gets fewer facts.
func (p *poll) switchMIBs(ifs []iface, ifErr error) {
	p.ports = map[string]*portExtra{}
	sc := p.staticData()
	if ifErr == nil && !fresh(sc.lldpAt, p.now) {
		base, nb, err := p.readLLDP(ifs)
		switch {
		case err != nil && isTimeout(err):
			p.partial("LLDP", err) // keep the old data, try again next poll
		default:
			sc.lldp, sc.basePort, sc.lldpAt = nb, base, p.now
			p.setStatic(func(c *staticCache) { c.lldp, c.basePort, c.lldpAt = nb, base, p.now })
		}
	}
	if ifErr == nil {
		for idx, n := range sc.lldp {
			p.extra(idx).neighbor = n
		}
	}
	if ifErr != nil {
		ifs = nil
	}
	p.readPoE(ifs, sc.basePort, sc.fpBase)
}

// ifIndexer finds interfaces by ifIndex and name.
type ifIndexer struct {
	byIndex  map[string]iface
	physical []iface // Ethernet ports (or rows without ifType) in ifIndex order
}

func newIfIndexer(ifs []iface) ifIndexer {
	ix := ifIndexer{byIndex: make(map[string]iface, len(ifs))}
	for _, f := range ifs {
		ix.byIndex[f.index] = f
		if isPhysical(f) {
			ix.physical = append(ix.physical, f)
		}
	}
	slices.SortStableFunc(ix.physical, func(a, b iface) int { return CompareOID(a.index, b.index) })
	return ix
}

// isPhysical reports whether an interface is a switch port: Ethernet, or a row without
// ifType (ports that only exist in ifXTable, UniFi US-8).
func isPhysical(f iface) bool {
	return isEthernet(f.typ) || (f.typ == 0 && !strings.HasPrefix(strings.ToLower(f.descr), "cpu interface"))
}

// port returns the ifIndex of a physical port, or "".
func (ix ifIndexer) port(idx string) string {
	if f, ok := ix.byIndex[idx]; ok && isPhysical(f) {
		return idx
	}
	return ""
}

// byName returns the ifIndex of the interface whose ifName or ifDescr (alias when
// alias is set) equals s, ignoring case; "" when none or several match.
func (ix ifIndexer) byName(s string, alias bool) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	found := ""
	for idx, f := range ix.byIndex {
		match := strings.EqualFold(f.name, s) || strings.EqualFold(f.descr, s)
		if alias {
			match = strings.EqualFold(f.alias, s)
		}
		if match {
			if found != "" && found != idx {
				return ""
			}
			found = idx
		}
	}
	return found
}

// readLLDP walks dot1dBasePortIfIndex and the LLDP remote table and maps each neighbor
// to the ifIndex of its local port: by lldpLocPortId/-Desc equal to ifName or ifDescr,
// then dot1dBasePortIfIndex[localPort] (UniFi US-8: base port 1 = ifIndex 1000001), then
// localPort as ifIndex (FASTPATH, Flex-XG), then by ifAlias.
func (p *poll) readLLDP(ifs []iface) (base map[string]string, nb map[string]neighbor, err error) {
	base = map[string]string{}
	bt, err := walkTable(p.s, oidDot1dBasePort, dot1dBasePortIfIndex)
	if err != nil && isTimeout(err) {
		return nil, nil, err
	}
	for _, r := range bt.rows {
		if v, ok := bt.uint(dot1dBasePortIfIndex, r); ok && v > 0 {
			base[r] = strconv.FormatUint(v, 10)
		}
	}
	rem, err := walkTable(p.s, oidLldpRemEntry, lldpRemChassisID, lldpRemChassisSubtype, lldpRemPortSubtype,
		lldpRemPortID, lldpRemPortDesc, lldpRemSysName)
	if err != nil {
		return base, map[string]neighbor{}, err
	}
	nb = map[string]neighbor{}
	if len(rem.rows) == 0 {
		return base, nb, nil
	}
	loc, err := walkTable(p.s, oidLldpLocPortEntry, lldpLocPortID, lldpLocPortDesc)
	if err != nil && isTimeout(err) {
		return nil, nil, err
	}
	ix := newIfIndexer(ifs)
	mapPort := func(lp string) string {
		for _, n := range []string{loc.str(lldpLocPortID, lp), loc.str(lldpLocPortDesc, lp)} {
			if idx := ix.byName(n, false); idx != "" {
				return idx
			}
		}
		if idx := ix.port(base[lp]); idx != "" {
			return idx
		}
		if idx := ix.port(lp); idx != "" {
			return idx
		}
		for _, n := range []string{loc.str(lldpLocPortID, lp), loc.str(lldpLocPortDesc, lp)} {
			if idx := ix.byName(n, true); idx != "" {
				return idx
			}
		}
		return ""
	}
	names := map[string][]string{}
	for _, r := range rem.rows {
		parts := strings.Split(r, ".") // timeMark.localPortNum.index
		if len(parts) != 3 {
			continue
		}
		idx := mapPort(parts[1])
		if idx == "" {
			continue
		}
		cSub, _ := rem.int(lldpRemChassisSubtype, r)
		pSub, _ := rem.int(lldpRemPortSubtype, r)
		name := rem.str(lldpRemSysName, r)
		if name == "" {
			name = lldpID(rem.cols[lldpRemChassisID][r], cSub == lldpChassisMAC)
		}
		port := lldpID(rem.cols[lldpRemPortID][r], pSub == lldpPortMAC)
		if desc := rem.str(lldpRemPortDesc, r); desc != "" && (port == "" || isMACText(port)) {
			port = desc
		}
		n := nb[idx]
		if name != "" && !slices.Contains(names[idx], name) {
			names[idx] = append(names[idx], name)
			if len(names[idx]) <= maxNeighbors {
				n.name = strings.Join(names[idx], ", ")
			} else {
				n.name = strings.Join(names[idx][:maxNeighbors], ", ") + " …"
			}
		}
		if n.port == "" {
			n.port = port
		}
		nb[idx] = n
	}
	return base, nb, nil
}

// lldpID renders an LLDP chassis or port id: six raw bytes (or a MAC subtype) as a MAC
// address "20:4C:9E:6E:08:FE", anything else as text.
func lldpID(pdu gosnmp.SnmpPDU, macSubtype bool) string {
	if missing(pdu) {
		return ""
	}
	if b, ok := pdu.Value.([]byte); ok && len(b) == 6 && (macSubtype || !printable(b)) {
		return fmtMAC(b)
	}
	return toString(pdu)
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func fmtMAC(b []byte) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(b)*3)
	for i, c := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[c>>4], hex[c&15])
	}
	return string(out)
}

// isMACText reports whether s looks like a MAC address ("20-4C-9E-6E-08-FE", "20:4c:…").
func isMACText(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i, r := range s {
		if i%3 == 2 {
			if r != ':' && r != '-' {
				return false
			}
		} else if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
			return false
		}
	}
	return true
}

// readPoE reads pethMainPseTable (budget, consumption) and pethPsePortTable (detection
// status, class) plus the vendor output power per port, maps the ports to interfaces and
// adds snmp:poe. Devices without POWER-ETHERNET-MIB get no PoE data.
func (p *poll) readPoE(ifs []iface, base map[string]string, fpBase string) {
	main, err := walkTable(p.s, oidPethMainPseEntry, pethMainPower, pethMainOper, pethMainConsumption)
	if err != nil {
		p.optional("PoE", err)
		return
	}
	ports, err := walkTable(p.s, oidPethPsePortEntry, pethPortDetection, pethPortAdmin, pethPortClass)
	if err != nil {
		p.optional("PoE", err)
		return
	}
	if len(main.rows) == 0 && len(ports.rows) == 0 {
		return
	}
	var vendor table
	if len(ports.rows) > 0 && (p.profile == ProfileUniFi || enterprise(p.sys.objectID) == 4413) {
		if fpBase == "" {
			fpBase = fpBase4413
		}
		vendor, err = walkTable(p.s, fpBase+fpPethPortEntry, fpPethOutputPower, fpPethFault)
		p.optional("PoE", err)
	}

	ix := newIfIndexer(ifs)
	groups := map[string]bool{}
	for _, r := range ports.rows {
		g, _, _ := strings.Cut(r, ".")
		groups[g] = true
	}
	var outSum float64
	hasOut := false
	delivering := 0
	var faults []string
	for n, r := range ports.rows {
		pp := &poePort{}
		pp.detect, _ = ports.int(pethPortDetection, r)
		pp.admin, _ = ports.int(pethPortAdmin, r)
		pp.class, _ = ports.int(pethPortClass, r)
		if mw, ok := vendor.num(fpPethOutputPower, r); ok && mw >= 0 {
			pp.mw, pp.hasPower = mw, true
			outSum += mw
			hasOut = true
		}
		pp.fault, pp.hasFault = vendor.int(fpPethFault, r)
		if pp.delivering() {
			delivering++
		}
		idx := poeIfIndex(r, n, len(groups) == 1, base, ix)
		if pp.faulty() {
			name := "Port " + r
			if f, ok := ix.byIndex[idx]; ok {
				name = f.name
			}
			faults = append(faults, name)
		}
		if idx != "" {
			p.extra(idx).poe = pp
		}
	}

	ck := model.Check{Key: "snmp:poe", Name: "PoE", Group: "PoE", State: model.StateOK, Facts: map[string]string{}}
	var budget, power float64
	hasBudget, hasPower, faulty, on := false, false, false, false
	for _, r := range main.rows {
		if v, ok := main.num(pethMainPower, r); ok && v > 0 {
			budget += v
			hasBudget = true
		}
		if v, ok := main.num(pethMainConsumption, r); ok && v >= 0 {
			power += v
			hasPower = true
		}
		switch s, _ := main.int(pethMainOper, r); s {
		case 1:
			on = true
		case 3:
			faulty = true
		}
	}
	if hasBudget && budget > 50_000 { // milliwatts (implausible as watts)
		budget /= 1000
	}
	if hasPower && hasBudget && power > budget*1.5 && power/1000 <= budget*1.5 {
		power /= 1000 // some agents report the consumption in mW
	}
	if !hasPower && hasOut {
		power, hasPower = outSum/1000, true
	}

	var parts []string
	if hasPower {
		ck.Metrics = append(ck.Metrics, model.Metric{Name: "poe.power", Value: power, Unit: "W"})
		parts = append(parts, "Verbrauch "+fmtNum(power, 1)+" W")
	}
	if hasBudget {
		ck.Metrics = append(ck.Metrics, model.Metric{Name: "poe.budget", Value: budget, Unit: "W"})
		ck.Facts["Budget"] = fmtNum(budget, 1) + " W"
		if hasPower {
			pct := power / budget * 100
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "poe.usage", Value: pct, Unit: "%"})
			parts[len(parts)-1] += " von " + fmtNum(budget, 1) + " W (" + fmtPct(pct) + ")"
		}
	}
	if len(ports.rows) > 0 {
		ck.Facts["PoE-Ports"] = strconv.Itoa(len(ports.rows))
		ck.Facts["Versorgte Geräte"] = strconv.Itoa(delivering)
		parts = append(parts, strconv.Itoa(delivering)+" Ports versorgt")
	}
	switch {
	case faulty:
		ck.State = model.StateCrit
		parts = append([]string{"PoE-Versorgung gestört"}, parts...)
	case len(main.rows) > 0 && !on:
		parts = append([]string{"PoE ausgeschaltet"}, parts...)
	}
	if len(faults) > 0 {
		ck.State = model.Worst(ck.State, model.StateWarn)
		parts = append(parts, "PoE-Fehler an "+strings.Join(faults, ", "))
	}
	ck.Message = strings.Join(parts, ", ")
	p.add(ck)
}

// poeIfIndex maps a pethPsePortTable row (group.port) to an ifIndex: with a single PSE
// group the port number through dot1dBasePortIfIndex, then as ifIndex; otherwise (or
// when neither is a physical port) the n-th physical port.
func poeIfIndex(row string, n int, oneGroup bool, base map[string]string, ix ifIndexer) string {
	_, port, ok := strings.Cut(row, ".")
	if ok && oneGroup {
		if idx := ix.port(base[port]); idx != "" {
			return idx
		}
		if idx := ix.port(port); idx != "" {
			return idx
		}
	}
	if n < len(ix.physical) {
		return ix.physical[n].index
	}
	return ""
}
