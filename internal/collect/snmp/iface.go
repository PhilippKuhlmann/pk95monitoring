package snmp

import (
	"fmt"
	"math"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// IF-MIB ifTable / ifXTable.
const (
	oidIfEntry  = "1.3.6.1.2.1.2.2.1"
	oidIfXEntry = "1.3.6.1.2.1.31.1.1.1"

	ifDescr, ifType, ifSpeed, ifAdminStatus, ifOperStatus        = 2, 3, 5, 7, 8
	ifInOctets, ifInErrors, ifOutOctets, ifOutErrors             = 10, 14, 16, 20
	ifName, ifHCInOctets, ifHCOutOctets, ifHighSpeed             = 1, 6, 10, 15
	ifAlias                                                      = 18
	ifTypeLoopback                                               = 24
	ifAdminDown                                                  = 2
	ifOperUp, ifOperDown, ifOperTesting, ifOperUnknown           = 1, 2, 3, 4
	ifOperDormant, ifOperNotPresent, ifOperLowerLayerDown        = 5, 6, 7
	maxInterfaces                                                = 512 // interfaces reported besides the watched ones
	ifSpeedSaturated                                      uint64 = math.MaxUint32
)

// ctr is one counter value with its width (32 or 64 bit); bits 0 = not available.
type ctr struct {
	v    uint64
	bits int
}

// iface is one row of the interface tables.
type iface struct {
	index              string
	name, descr, alias string
	typ, admin, oper   int    // 0 = not readable
	speed              uint64 // bit/s
	in, out            ctr    // octets
	inErr, outErr      ctr
}

func isEthernet(typ int) bool {
	switch typ {
	case 6, 62, 69, 117: // ethernetCsmacd, fastEther, fastEtherFX, gigabitEthernet
		return true
	}
	return false
}

func operName(oper int) string {
	switch oper {
	case ifOperUp:
		return "up"
	case ifOperDown:
		return "down"
	case ifOperTesting:
		return "testing"
	case ifOperDormant:
		return "dormant"
	case ifOperNotPresent:
		return "notPresent"
	case ifOperLowerLayerDown:
		return "lowerLayerDown"
	}
	return "unknown"
}

// operGerman is the German operational status (fact "Status"); 0 = not readable.
var operGerman = map[int]string{
	0: "nicht lesbar", ifOperUp: "verbunden", ifOperDown: "getrennt", ifOperTesting: "Test",
	ifOperUnknown: "unbekannt", ifOperDormant: "ruhend", ifOperNotPresent: "nicht vorhanden",
	ifOperLowerLayerDown: "untere Schicht getrennt",
}

// readInterfaces reads ifTable and ifXTable. 64-bit counters are used when available.
func readInterfaces(s Session) ([]iface, error) {
	t, err := walkTable(s, oidIfEntry, ifDescr, ifType, ifSpeed, ifAdminStatus, ifOperStatus,
		ifInOctets, ifInErrors, ifOutOctets, ifOutErrors)
	if err != nil {
		return nil, err
	}
	x, err := walkTable(s, oidIfXEntry, ifName, ifHCInOctets, ifHCOutOctets, ifHighSpeed, ifAlias)
	if err != nil {
		return nil, err
	}
	rows := t.rows
	for _, r := range x.rows {
		if _, ok := t.cols[ifDescr][r]; !ok {
			rows = append(rows, r)
		}
	}

	out := make([]iface, 0, len(rows))
	names := map[string]bool{}
	for _, r := range rows {
		f := iface{index: r, descr: t.str(ifDescr, r), alias: x.str(ifAlias, r)}
		f.typ, _ = t.int(ifType, r)
		f.admin, _ = t.int(ifAdminStatus, r)
		f.oper, _ = t.int(ifOperStatus, r)
		f.name = x.str(ifName, r)
		if f.name == "" {
			f.name = f.descr
		}
		if f.name == "" {
			f.name = "if" + r
		}
		if names[strings.ToLower(f.name)] {
			f.name += " (" + r + ")"
		}
		names[strings.ToLower(f.name)] = true

		if hs, ok := x.uint(ifHighSpeed, r); ok && hs > 0 {
			f.speed = hs * 1_000_000
		} else if sp, ok := t.uint(ifSpeed, r); ok && sp != ifSpeedSaturated {
			f.speed = sp
		}
		f.in = counter(x, ifHCInOctets, t, ifInOctets, r)
		f.out = counter(x, ifHCOutOctets, t, ifOutOctets, r)
		f.inErr = counter(table{}, 0, t, ifInErrors, r)
		f.outErr = counter(table{}, 0, t, ifOutErrors, r)
		out = append(out, f)
	}
	return out, nil
}

// counter prefers the 64-bit column of hc and falls back to the 32-bit column of t.
func counter(hc table, hcCol int, t table, col int, row string) ctr {
	if v, ok := hc.uint(hcCol, row); ok {
		return ctr{v: v, bits: 64}
	}
	if v, ok := t.uint(col, row); ok {
		return ctr{v: v & math.MaxUint32, bits: 32}
	}
	return ctr{}
}

// interfaces produces snmp:if:<name> for every interface except loopbacks (at most
// maxInterfaces besides the watched ones). Ports may go offline: a port without link is ok
// ("Kein Link", "Deaktiviert"); only the watched ports (cfg.Interfaces) are crit when their
// link is not up. Traffic metrics are recorded only while a port is up.
func (p *poll) interfaces(ifs []iface, err error) {
	var watchList []string
	watch := map[string]bool{} // lower-case configured names
	for _, w := range p.cfg.Interfaces {
		if w = strings.TrimSpace(w); w != "" && !watch[strings.ToLower(w)] {
			watch[strings.ToLower(w)] = true
			watchList = append(watchList, w)
		}
	}
	p.c.mu.Lock()
	defer p.c.mu.Unlock()
	st := p.c.state[p.key]
	// A watched interface that cannot be read keeps the key (ifName) it had in the last
	// poll; a different key would make its check disappear and close its alert silently.
	missingCheck := func(w, msg string) model.Check {
		name := w
		if st != nil && st.watch[strings.ToLower(w)] != "" {
			name = st.watch[strings.ToLower(w)]
		}
		return model.Check{Key: "snmp:if:" + name, Name: "Port " + name, Group: "Schnittstellen",
			State: model.StateUnknown, Message: msg, Facts: map[string]string{"Alarm bei Link-Ausfall": "ja"}}
	}
	if err != nil {
		p.partial("Schnittstellen", err)
		emitted := map[string]bool{}
		for _, w := range watchList {
			if ck := missingCheck(w, "Abfrage fehlgeschlagen"); !emitted[ck.Key] {
				emitted[ck.Key] = true
				p.add(ck)
			}
		}
		return
	}

	found := map[string]bool{}
	others, skipped := 0, 0
	for _, f := range ifs {
		watched := false
		for _, n := range []string{f.name, f.descr} {
			if n != "" && watch[strings.ToLower(n)] {
				watched = true
				found[strings.ToLower(n)] = true
				if st != nil {
					if st.watch == nil {
						st.watch = map[string]string{}
					}
					st.watch[strings.ToLower(n)] = f.name
				}
			}
		}
		if !watched {
			if f.typ == ifTypeLoopback || (switchLike(p.profile) && idleVirtual(f, p.profile)) {
				continue
			}
			if others >= maxInterfaces {
				skipped++
				continue
			}
			others++
		}
		ck := ifCheck(f, watched)
		// Rows only in ifXTable (no ifOperStatus, e.g. the ports of a UniFi US-8) still
		// get traffic charts when they have 64-bit counters.
		if f.oper == ifOperUp || (f.oper == 0 && f.in.bits == 64) {
			ck.Metrics = st.ifMetrics(f, p.now)
		} else {
			st.ifPause(f, p.now)
		}
		if x := p.ports[f.index]; x != nil {
			x.decorate(&ck, f)
		}
		p.add(ck)
	}
	if skipped > 0 {
		p.facts["Schnittstellen"] = fmt.Sprintf("%d von %d erfasst (Obergrenze %d)",
			others, others+skipped, maxInterfaces)
	}
	emitted := map[string]bool{}
	for _, ck := range p.checks {
		emitted[ck.Key] = true
	}
	for _, w := range watchList {
		if !found[strings.ToLower(w)] {
			if ck := missingCheck(w, "Schnittstelle nicht gefunden"); !emitted[ck.Key] {
				emitted[ck.Key] = true
				p.add(ck)
			}
		}
	}
}

// idleVirtual reports whether an interface of a switch is not worth a check: the FASTPATH
// "CPU Interface", link aggregates, VLAN, tunnel and other virtual interfaces that are not
// up and, on UniFi switches, rows that are not present (unused aggregates, stack ports).
func idleVirtual(f iface, profile string) bool {
	if f.oper == ifOperNotPresent && profile == ProfileUniFi {
		return true
	}
	if strings.HasPrefix(strings.ToLower(f.descr), "cpu interface") {
		return true
	}
	switch f.typ {
	case 1, 22, 53, 131, 161: // other (FASTPATH LAGs), propPointToPointSerial, propVirtual, tunnel, ieee8023adLag
		return f.oper != ifOperUp
	}
	return false
}

// ifCheck builds the check of one interface (without metrics). A watched port is crit
// when its link is not up and unknown when its status cannot be read; every other port
// is ok whatever its link does.
func ifCheck(f iface, watched bool) model.Check {
	ck := model.Check{Key: "snmp:if:" + f.name, Name: "Port " + f.name, Group: "Schnittstellen",
		State: model.StateOK, Facts: map[string]string{"Status": operGerman[ifOperUnknown]}}
	if g, ok := operGerman[f.oper]; ok {
		ck.Facts["Status"] = g
	}
	if f.descr != "" && f.descr != f.name {
		ck.Facts["Beschreibung"] = f.descr
	}
	if f.alias != "" {
		ck.Facts["Alias"] = f.alias
	}
	if f.speed > 0 {
		ck.Facts["Geschwindigkeit"] = fmtSpeed(f.speed)
	}
	if watched {
		ck.Facts["Alarm bei Link-Ausfall"] = "ja"
	}
	adminDown := f.admin == ifAdminDown && f.oper != ifOperUp
	if adminDown {
		ck.Facts["Admin"] = "deaktiviert"
	}
	switch {
	case f.oper == ifOperUp:
		ck.Message = "Verbunden"
		if f.speed > 0 {
			ck.Message += " mit " + fmtSpeed(f.speed)
		}
		if f.alias != "" {
			ck.Message += " (" + f.alias + ")"
		}
	case adminDown && watched:
		ck.State, ck.Message = model.StateCrit, "Port deaktiviert"
	case adminDown:
		ck.Message = "Deaktiviert"
	case watched && (f.oper == 0 || f.oper == ifOperUnknown): // row only in ifXTable, or "unknown"
		ck.State, ck.Message = model.StateUnknown, "Linkstatus nicht lesbar"
	case watched:
		ck.State, ck.Message = model.StateCrit, "Kein Link"
	case f.oper == ifOperDormant:
		ck.Message = "Ruhend"
	case f.oper == ifOperTesting:
		ck.Message = "Testmodus"
	case f.oper == 0 || f.oper == ifOperUnknown:
		ck.Message = "Status unbekannt"
	default: // down, notPresent, lowerLayerDown
		ck.Message = "Kein Link"
	}
	return ck
}

// ifPause moves the stored counter samples of a port that is not up to the current poll
// without producing a rate, so the first rate after the link comes back covers only the
// last poll interval (no average over the whole outage, no spike from a counter that
// moved meanwhile) and the samples do not expire during a long outage.
// The caller holds the collector lock.
func (st *devState) ifPause(f iface, now time.Time) {
	if st == nil {
		return
	}
	for _, c := range []struct {
		key string
		c   ctr
	}{{"in", f.in}, {"out", f.out}, {"inerr", f.inErr}, {"outerr", f.outErr}} {
		key := "if:" + f.index + ":" + c.key
		if c.c.bits == 0 {
			delete(st.ctr, key)
			continue
		}
		if prev, ok := st.ctr[key]; ok && now.Equal(prev.t) {
			continue // same poll time: keep the older sample, as rate does
		}
		st.ctr[key] = sample{v: c.c.v, bits: c.c.bits, t: now}
	}
}

// ifMetrics turns the counters of an interface into if.rx, if.tx (B/s) and if.errors (1/s).
// The caller holds the collector lock.
func (st *devState) ifMetrics(f iface, now time.Time) []model.Metric {
	if st == nil {
		return nil
	}
	maxRate := float64(f.speed) / 8 // bytes per second at line rate
	var ms []model.Metric
	if r, ok := st.rate("if:"+f.index+":in", f.in, now, maxRate); ok {
		ms = append(ms, model.Metric{Name: "if.rx", Instance: f.name, Value: r, Unit: "B/s"})
	}
	if r, ok := st.rate("if:"+f.index+":out", f.out, now, maxRate); ok {
		ms = append(ms, model.Metric{Name: "if.tx", Instance: f.name, Value: r, Unit: "B/s"})
	}
	ri, okIn := st.rate("if:"+f.index+":inerr", f.inErr, now, 0)
	ro, okOut := st.rate("if:"+f.index+":outerr", f.outErr, now, 0)
	if okIn || okOut {
		ms = append(ms, model.Metric{Name: "if.errors", Instance: f.name, Value: ri + ro, Unit: "1/s"})
	}
	return ms
}

// rate returns the per-second increase of a counter since the previous sample. The first
// sample, a changed counter width, a 64-bit counter going back (reset) and implausible
// values (faster than maxRate, if > 0) give no rate. A 32-bit counter going back is
// taken as one wrap.
func (st *devState) rate(key string, c ctr, now time.Time, maxRate float64) (float64, bool) {
	if c.bits == 0 {
		return 0, false
	}
	prev, ok := st.ctr[key]
	if ok && now.Equal(prev.t) {
		return 0, false // same poll time: keep the older sample
	}
	st.ctr[key] = sample{v: c.v, bits: c.bits, t: now}
	if !ok || prev.bits != c.bits || now.Before(prev.t) { // clock set back: start over
		return 0, false
	}
	var delta uint64
	switch {
	case c.v >= prev.v:
		delta = c.v - prev.v
	case c.bits == 32:
		delta = (math.MaxUint32 - prev.v) + c.v + 1
		if maxRate <= 0 && delta >= 1<<31 {
			return 0, false // more likely a reset than a wrap
		}
	default:
		return 0, false
	}
	r := float64(delta) / now.Sub(prev.t).Seconds()
	if maxRate > 0 && r > maxRate*1.1 {
		return 0, false
	}
	return r, true
}
