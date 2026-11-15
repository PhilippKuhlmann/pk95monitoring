package snmp

import (
	"math"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// SFOS-FIREWALL-MIB (Sophos Firewall / XG, SFOS) below 1.3.6.1.4.1.2604.5.1.
const (
	sfos = "1.3.6.1.4.1.2604.5.1."

	sfosDeviceType = sfos + "1.2.0"
	sfosFWVersion  = sfos + "1.3.0"
	sfosAppKey     = sfos + "1.4.0"
	sfosDiskCap    = sfos + "2.4.1.0" // MB
	sfosDiskPct    = sfos + "2.4.2.0"
	sfosMemCap     = sfos + "2.5.1.0" // MB
	sfosMemPct     = sfos + "2.5.2.0"
	sfosSwapPct    = sfos + "2.5.4.0"
	sfosLiveUsers  = sfos + "2.6.0"

	sfosHAStatus    = sfos + "4.1.0" // 0 disabled, 1 enabled
	sfosHAState     = sfos + "4.4.0" // HaState of this device
	sfosHAPeerState = sfos + "4.5.0"
	sfosHAMode      = sfos + "4.6.0"
	sfosHAPeerIP    = sfos + "4.10.0"

	sfosLicense  = sfos + "5."        // + n.1.0 (SubscriptionStatusType), n.2.0 (expiry date)
	sfosIPSecRow = sfos + "6.1.1.1.1" // sfosIPSecVpnTunnelEntry
	sfosServices = sfos + "3."        // + n.0 (ServiceStatsType)

	ipsecName, ipsecDescr, ipsecPolicy, ipsecMode, ipsecType, ipsecGwPort, ipsecStatus, ipsecActivated = 2, 3, 4, 5, 6, 7, 9, 10
)

// sfosLicenses are the subscriptions of sfosXGLicenseDetails (index n of sfosLicense).
var sfosLicenses = []struct {
	n    int
	name string
}{
	{1, "Base Firewall"}, {2, "Network Protection"}, {3, "Web Protection"}, {4, "Email Protection"},
	{5, "Webserver Protection"}, {6, "Sandstorm"}, {7, "Enhanced Support"}, {8, "Enhanced Plus Support"},
	{9, "Central Orchestration"},
}

// sfosHAStates is the HaState enumeration.
var sfosHAStates = map[int]string{0: "nicht zutreffend", 1: "Hilfsgerät (Auxiliary)", 2: "Einzelgerät (Standalone)",
	3: "Primär", 4: "Fehlerhaft", 5: "Bereit"}

// sfosServiceNames are the services of sfosXGServiceStatus by index; core ones must run.
var sfosServiceNames = map[int]struct {
	name string
	core bool
}{
	1: {"POP3", false}, 2: {"IMAP4", false}, 3: {"SMTP", false}, 4: {"FTP", false}, 5: {"HTTP-Proxy", false},
	6: {"Virenschutz", false}, 7: {"Spamschutz", false}, 8: {"DNS", false}, 9: {"Hochverfügbarkeit", false},
	10: {"IPS", false}, 11: {"Webadmin (Apache)", true}, 12: {"NTP", false}, 13: {"Tomcat", false},
	14: {"SSL-VPN", false}, 15: {"IPsec-VPN", false}, 16: {"Datenbank", true}, 17: {"Netzwerk", true},
	18: {"Protokollierung (Garner)", true}, 19: {"Dynamisches Routing", false}, 20: {"SSH", false}, 21: {"DGD", false},
}

// sophos reads the SFOS MIB: model, firmware, live users into snmp:system, memory and disk
// (when HOST-RESOURCES-MIB had none), snmp:ha, snmp:lic:<name>, snmp:vpn:<name> and
// snmp:services.
func (p *poll) sophos() {
	v, err := get(p.s, sfosDeviceType, sfosFWVersion, sfosAppKey, sfosDiskCap, sfosDiskPct, sfosMemCap, sfosMemPct,
		sfosSwapPct, sfosLiveUsers, sfosHAStatus, sfosHAState, sfosHAPeerState, sfosHAMode, sfosHAPeerIP)
	if err != nil {
		p.partial("Sophos", err)
		return
	}
	if len(v) == 0 {
		p.problem(model.StateUnknown, "Keine Sophos-Daten (SFOS-FIREWALL-MIB) gefunden")
		return
	}
	p.facts["Modell"] = v.str(sfosDeviceType)
	p.facts["Firmware"] = v.str(sfosFWVersion)
	p.facts["Appliance-Key"] = v.str(sfosAppKey)
	if n, ok := v.num(sfosLiveUsers); ok {
		p.facts["Angemeldete Benutzer"] = fmtNum(n, 0)
	}
	if pct, ok := v.num(sfosMemPct); ok && !p.has("snmp:mem") {
		ck := model.Check{Key: "snmp:mem", Name: "Arbeitsspeicher", Group: "System", State: model.StateOK,
			Message: fmtPct(pct) + " belegt", Metrics: []model.Metric{{Name: "mem.usage", Value: pct, Unit: "%"}}}
		if mb, ok := v.num(sfosMemCap); ok && mb > 0 {
			ck.Message = fmtBytes(mb*pct/100*(1<<20)) + " von " + fmtBytes(mb*(1<<20)) + " belegt (" + fmtPct(pct) + ")"
		}
		if sw, ok := v.num(sfosSwapPct); ok {
			ck.Metrics = append(ck.Metrics, model.Metric{Name: "swap.usage", Value: sw, Unit: "%"})
			ck.Facts = map[string]string{"Auslagerung": fmtPct(sw)}
		}
		p.add(ck)
	}
	if pct, ok := v.num(sfosDiskPct); ok && !p.has("snmp:storage:Firewall") {
		ck := model.Check{Key: "snmp:storage:Firewall", Name: "Speicher Firewall", Group: "Speicher", State: model.StateOK,
			Message: fmtPct(pct) + " belegt", Metrics: []model.Metric{{Name: "disk.usage", Instance: "Firewall", Value: pct, Unit: "%"}}}
		if mb, ok := v.num(sfosDiskCap); ok && mb > 0 {
			ck.Message = fmtBytes(mb*pct/100*(1<<20)) + " von " + fmtBytes(mb*(1<<20)) + " belegt (" + fmtPct(pct) + ")"
		}
		p.add(ck)
	}
	p.sophosHA(v)
	p.sophosLicenses()
	p.sophosVPN()
	p.sophosServices()
}

func (p *poll) sophosHA(v vals) {
	if on, ok := v.int(sfosHAStatus); !ok || on != 1 {
		return
	}
	cur, _ := v.int(sfosHAState)
	peer, okPeer := v.int(sfosHAPeerState)
	ck := model.Check{Key: "snmp:ha", Name: "Hochverfügbarkeit", Group: "Hochverfügbarkeit", State: model.StateOK,
		Facts: map[string]string{"Dieses Gerät": sfosHAStates[cur]}}
	if okPeer {
		ck.Facts["Partner"] = sfosHAStates[peer]
	}
	if m := v.str(sfosHAMode); m != "" {
		ck.Facts["Modus"] = m
	}
	if ip := v.str(sfosHAPeerIP); ip != "" {
		ck.Facts["Partner-IP"] = ip
	}
	switch {
	case cur == 4:
		ck.State, ck.Message = model.StateCrit, "Dieses Gerät ist fehlerhaft (faulty)"
	case peer == 4:
		ck.State, ck.Message = model.StateCrit, "Der HA-Partner ist fehlerhaft (faulty)"
	case cur == 2 || !okPeer || peer == 0 || peer == 2:
		ck.State, ck.Message = model.StateCrit, "HA eingeschaltet, aber kein Partner – dieses Gerät läuft allein"
	case cur == 5 || peer == 5:
		ck.State, ck.Message = model.StateWarn, "HA-Cluster synchronisiert noch (bereit, nicht aktiv)"
	default:
		ck.Message = sfosHAStates[cur] + ", Partner " + sfosHAStates[peer]
	}
	p.add(ck)
}

// sfosSubscription is the SubscriptionStatusType enumeration.
var sfosSubscription = map[int]string{0: "keine", 1: "Testversion", 2: "nicht abonniert", 3: "abonniert", 4: "abgelaufen", 5: "deaktiviert"}

// licenseForever: an expiry further away is shown as unlimited (perpetual base licenses).
const licenseForever = 20 * 365

func (p *poll) sophosLicenses() {
	oids := make([]string, 0, 2*len(sfosLicenses))
	for _, l := range sfosLicenses {
		n := strconv.Itoa(l.n)
		oids = append(oids, sfosLicense+n+".1.0", sfosLicense+n+".2.0")
	}
	v, err := get(p.s, oids...)
	if err != nil {
		p.optional("Lizenzen", err)
		return
	}
	for _, l := range sfosLicenses {
		n := strconv.Itoa(l.n)
		st, ok := v.int(sfosLicense + n + ".1.0")
		if !ok || st == 0 || st == 2 { // not licensed: no check
			continue
		}
		raw := strings.TrimSpace(v.str(sfosLicense + n + ".2.0"))
		ck := model.Check{Key: "snmp:lic:" + l.name, Name: "Lizenz " + l.name, Group: "Lizenzen", State: model.StateOK,
			Facts: map[string]string{"Status": sfosSubscription[st], "Produkt": "Sophos " + l.name}}
		if raw != "" {
			ck.Facts["Läuft ab"] = raw
		}
		until, okDate := parseLicenseDate(raw)
		days := 0.0
		if okDate {
			days = math.Floor(until.Sub(p.now).Hours() / 24)
			if days < licenseForever {
				ck.Facts["Läuft ab"] = until.Format("02.01.2006")
				ck.Metrics = []model.Metric{{Name: "license.days", Instance: l.name, Value: days, Unit: "d"}}
			}
		}
		switch {
		case st == 4:
			ck.State, ck.Message = model.StateCrit, "abgelaufen"
			if okDate && days < licenseForever {
				ck.Message += " seit " + until.Format("02.01.2006")
			}
		case st == 5:
			ck.State, ck.Message = model.StateWarn, "deaktiviert"
		case okDate && days >= licenseForever, perpetual(raw):
			ck.Message = "unbefristet"
		case okDate:
			ck.Message = "gültig bis " + until.Format("02.01.2006") + " (" + fmtNum(max(days, 0), 0) + " Tage)"
		case raw != "":
			ck.Message = "gültig bis " + raw
		default:
			ck.Message = sfosSubscription[st]
		}
		if st == 1 {
			ck.Message = "Testversion · " + ck.Message
		}
		p.add(ck)
	}
}

// perpetual reports whether an expiry text means "never expires".
func perpetual(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "perpetual") || strings.Contains(s, "unlimited") || strings.Contains(s, "never")
}

// licenseLayouts are the expiry date formats SFOS has been seen to use.
var licenseLayouts = []string{"Jan 2 2006", "Jan _2 2006", "Jan 2, 2006", "2 Jan 2006", "02-Jan-2006", "2006-01-02",
	"02.01.2006", "01/02/2006", "Mon Jan 2 2006", "Mon Jan _2 15:04:05 2006", "Jan 2 15:04:05 2006"}

func parseLicenseDate(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range licenseLayouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var sfosIPSecTypes = map[int]string{1: "Host-zu-Host", 2: "Site-to-Site", 3: "Tunnel-Schnittstelle"}

func (p *poll) sophosVPN() {
	t, err := walkTable(p.s, sfosIPSecRow, ipsecName, ipsecDescr, ipsecPolicy, ipsecMode, ipsecType, ipsecGwPort, ipsecStatus, ipsecActivated)
	if err != nil {
		p.optional("IPsec", err)
		return
	}
	for _, r := range t.rows {
		name := t.str(ipsecName, r)
		if name == "" {
			continue
		}
		ck := model.Check{Key: "snmp:vpn:" + name, Name: "VPN " + name, Group: "VPN", State: model.StateOK, Facts: map[string]string{}}
		for col, label := range map[int]string{ipsecDescr: "Beschreibung", ipsecPolicy: "Richtlinie", ipsecMode: "Modus", ipsecGwPort: "Gateway"} {
			if s := t.str(col, r); s != "" {
				ck.Facts[label] = s
			}
		}
		typ, _ := t.int(ipsecType, r)
		if s, ok := sfosIPSecTypes[typ]; ok {
			ck.Facts["Typ"] = s
		}
		act, _ := t.int(ipsecActivated, r)
		st, _ := t.int(ipsecStatus, r)
		switch {
		case act != 1:
			ck.Message = "nicht aktiviert"
		case st == 1:
			ck.Message = "verbunden"
		case st == 2:
			ck.State, ck.Message = model.StateWarn, "teilweise verbunden – nicht alle Tunnel (Netze) stehen"
		case typ == 2 || typ == 3:
			ck.State, ck.Message = model.StateCrit, "getrennt"
		default: // host-to-host connections come up on demand
			ck.Message = "nicht verbunden"
		}
		p.add(ck)
	}
}

// sfosServiceStates is the ServiceStatsType enumeration.
var sfosServiceStates = map[int]string{0: "unberührt", 1: "gestoppt", 2: "startet", 3: "läuft", 4: "beendet sich",
	5: "abgestürzt", 6: "eingefroren", 7: "nicht registriert"}

func (p *poll) sophosServices() {
	oids := make([]string, 0, len(sfosServiceNames))
	for i := 1; i <= len(sfosServiceNames); i++ {
		oids = append(oids, sfosServices+strconv.Itoa(i)+".0")
	}
	v, err := get(p.s, oids...)
	if err != nil {
		p.optional("Dienste", err)
		return
	}
	if len(v) == 0 {
		return
	}
	ck := model.Check{Key: "snmp:services", Name: "Firewall-Dienste", Group: "Dienste", State: model.StateOK, Facts: map[string]string{}}
	var bad []string
	running := 0
	for i := 1; i <= len(sfosServiceNames); i++ {
		st, ok := v.int(sfosServices + strconv.Itoa(i) + ".0")
		if !ok {
			continue
		}
		svc := sfosServiceNames[i]
		ck.Facts[svc.name] = sfosServiceStates[st]
		switch {
		case st == 3:
			running++
		case st == 5 || st == 6:
			ck.State = model.StateCrit
			bad = append(bad, svc.name+" "+sfosServiceStates[st])
		case svc.core && (st == 1 || st == 4):
			ck.State = model.Worst(ck.State, model.StateWarn)
			bad = append(bad, svc.name+" "+sfosServiceStates[st])
		}
	}
	ck.Message = strconv.Itoa(running) + " Dienste laufen"
	if len(bad) > 0 {
		ck.Message = strings.Join(bad, ", ") + " · " + ck.Message
	}
	ck.Metrics = []model.Metric{{Name: "services.failed", Value: float64(len(bad))}}
	p.add(ck)
}
