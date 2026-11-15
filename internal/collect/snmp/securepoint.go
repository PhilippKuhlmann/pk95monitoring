package snmp

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

// SECUREPOINT-MIB and SECUREPOINT-UTM-MIB (Securepoint UTM) below 1.3.6.1.4.1.28553, as
// documented in the Securepoint wiki (UTM/NET/SNMP-OIDs, version 14.0.1) and checked against a
// UTM 14.1.9. The UTM serves its scalars without the ".0" instance suffix (comProdName is
// 28553.1.1, not 28553.1.1.0); both forms are read. The wiki names the objects but not every
// column number, so tables are read by content where it is not given (cluster values,
// WireGuard handshake timestamps …).
const (
	spProdName  = "1.3.6.1.4.1.28553.1.1"       // comProdName: "utm"
	spVersion   = "1.3.6.1.4.1.28553.1.2"       // comVerEpoch … comVersion.7 = "14.1.9.3"
	spLicense   = "1.3.6.1.4.1.28553.1.3"       // comLicLicensee, Email, Type, UsersMax, DaysLeft, Serial, …
	spApps      = "1.3.6.1.4.1.28553.2.1.1.1.1" // appStatusEntry: 1 index, 2 name, 3 status, 4 flags
	spIPsec     = "1.3.6.1.4.1.28553.2.1.2.1.1" // ipsecStatusTable: 2 name, 3 status (0 unknown, 1 up, 2 down)
	spIPsecConn = "1.3.6.1.4.1.28553.2.1.2.2.1" // ipsecConnectionTable: 6 ipsecSubnetStatus up/down
	spAVPattern = "1.3.6.1.4.1.28553.2.1.5.1.1" // avPatternStatusEntry: index, product, last update, last run, state
	spConntrack = "1.3.6.1.4.1.28553.2.1.10"    // .1 max, .2 count
	spWGPeer    = "1.3.6.1.4.1.28553.2.1.11.2.1"
	spCluster   = "1.3.6.1.4.1.28553.2.1.12"

	ucdCPUIdle = "1.3.6.1.4.1.2021.11.11.0"
	ucdLoad1   = "1.3.6.1.4.1.2021.10.1.3.1"

	spIPsecName, spIPsecStatus = 2, 3
	spSubnetStatus             = 6
	spAppName, spAppStatus     = 2, 3
	spAVProduct, spAVUpdate    = 2, 3
	spAVRun, spAVState         = 4, 5

	// licenseUnlimited: comLicDaysLeft of a license without end is 0xffffffff.
	licenseUnlimited = 1 << 31
	// wgStale: a WireGuard peer without handshake for longer is not connected (keepalive 25 s,
	// rekey after 2 minutes).
	wgStale = 5 * time.Minute
)

// spAppStates are the known appStatus values: the wiki shows error(4) and eNoService(9), a UTM
// 14.1 shows 1 for running services and 2 for stopped ones (OpenVPN, IPsec … not set up).
var spAppStates = map[int]string{1: "läuft", 2: "gestoppt", 4: "Fehler", 9: "nicht eingerichtet"}

// rowTable is a walked table: column → row → value, rows in OID order.
type rowTable struct {
	rows []string
	cols map[int]map[string]gosnmp.SnmpPDU
}

// walkRows walks a whole table entry (all columns at once).
func walkRows(s Session, entry string) (rowTable, error) {
	t := rowTable{cols: map[int]map[string]gosnmp.SnmpPDU{}}
	pdus, err := s.Walk(entry)
	if err != nil {
		return t, err
	}
	seen := map[string]bool{}
	for _, p := range pdus {
		rest, ok := strings.CutPrefix(trimDot(p.Name), entry+".")
		if !ok || missing(p) {
			continue
		}
		c, row, ok := strings.Cut(rest, ".")
		col, err := strconv.Atoi(c)
		if !ok || err != nil {
			continue
		}
		if t.cols[col] == nil {
			t.cols[col] = map[string]gosnmp.SnmpPDU{}
		}
		t.cols[col][row] = p
		if !seen[row] {
			seen[row] = true
			t.rows = append(t.rows, row)
		}
	}
	slices.SortFunc(t.rows, CompareOID)
	return t, nil
}

func (t rowTable) str(col int, row string) string { return toString(t.cols[col][row]) }

func (t rowTable) int(col int, row string) (int, bool) {
	p, ok := t.cols[col][row]
	if !ok {
		return 0, false
	}
	return toInt(p)
}

// sortedCols returns the column numbers in order.
func (t rowTable) sortedCols() []int {
	cols := make([]int, 0, len(t.cols))
	for c := range t.cols {
		cols = append(cols, c)
	}
	slices.Sort(cols)
	return cols
}

// texts returns the text values of a row in column order.
func (t rowTable) texts(row string) []string {
	var out []string
	for _, c := range t.sortedCols() {
		if p, ok := t.cols[c][row]; ok && p.Type == gosnmp.OctetString {
			out = append(out, toString(p))
		}
	}
	return out
}

// subtree walks a subtree and returns its values in OID order.
func subtree(s Session, root string) ([]gosnmp.SnmpPDU, error) {
	pdus, err := s.Walk(root)
	if err != nil {
		return nil, err
	}
	out := make([]gosnmp.SnmpPDU, 0, len(pdus))
	for _, p := range pdus {
		if !missing(p) && strings.HasPrefix(trimDot(p.Name), root+".") {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b gosnmp.SnmpPDU) int { return CompareOID(trimDot(a.Name), trimDot(b.Name)) })
	return out, nil
}

// securepoint reads the Securepoint MIBs: version and license into snmp:system and
// snmp:lic:UTM, CPU and memory from UCD-SNMP when HOST-RESOURCES-MIB had none, snmp:cluster,
// snmp:vpn:<name> (IPsec), snmp:ipsec-subnets, snmp:wg:<peer>, snmp:services, snmp:av and
// snmp:conntrack.
func (p *poll) securepoint() {
	p.facts["Produkt"] = "Securepoint UTM"
	p.mergeBindMounts()
	if ver, err := subtree(p.s, spVersion); err != nil {
		p.optional("Version", err)
	} else if v := spFirmware(ver); v != "" {
		p.facts["Firmware"] = v
	}
	p.securepointLicense()
	p.ucdResources()
	p.securepointCluster()
	p.securepointIPsec()
	p.securepointWireGuard()
	p.securepointServices()
	p.securepointConntrack()
}

// mergeBindMounts drops the storage checks of bind mounts: the UTM lists /data, /home and /root
// (one file system) and /etc and /etc/machine-id separately. Storages with the same size and
// the same used bytes (> 0) are one file system; the shortest path stays and names the others.
// Only for this profile: on other devices it would turn existing checks into vanished ones.
func (p *poll) mergeBindMounts() {
	type fs struct{ total, used float64 }
	first := map[fs]int{}
	drop := map[int]bool{}
	for i, ck := range p.checks {
		if !strings.HasPrefix(ck.Key, "snmp:storage:") {
			continue
		}
		var f fs
		for _, m := range ck.Metrics {
			switch m.Name {
			case "disk.total":
				f.total = m.Value
			case "disk.used":
				f.used = m.Value
			}
		}
		if f.used <= 0 || f.total <= 0 {
			continue
		}
		j, dup := first[f]
		if !dup {
			first[f] = i
			continue
		}
		keep, other := j, i
		if a, b := p.checks[i].Key, p.checks[j].Key; len(a) < len(b) || (len(a) == len(b) && a < b) {
			keep, other = i, j
		}
		first[f], drop[other] = keep, true
		path := strings.TrimPrefix(p.checks[other].Key, "snmp:storage:")
		if also := p.checks[other].Facts["Auch eingehängt unter"]; also != "" {
			path += ", " + also
		}
		if p.checks[keep].Facts == nil {
			p.checks[keep].Facts = map[string]string{}
		}
		if also := p.checks[keep].Facts["Auch eingehängt unter"]; also != "" {
			path = also + ", " + path
		}
		p.checks[keep].Facts["Auch eingehängt unter"] = path
	}
	if len(drop) == 0 {
		return
	}
	kept := p.checks[:0]
	for i, ck := range p.checks {
		if !drop[i] {
			kept = append(kept, ck)
		}
	}
	p.checks = kept
}

// spFirmware is comVersion.7 or epoch.major.minor from the version subtree.
func spFirmware(pdus []gosnmp.SnmpPDU) string {
	var nums []string
	for _, pd := range pdus {
		name := trimDot(pd.Name)
		if pd.Type == gosnmp.OctetString {
			s := toString(pd)
			if s != "" && s != "none" && s != "(null)" && (strings.HasSuffix(name, ".1.2.7") || strings.HasSuffix(name, ".1.2.7.0")) {
				return s
			}
			continue
		}
		if n, ok := toFloat(pd); ok && len(nums) < 3 {
			nums = append(nums, fmtNum(n, 0))
		}
	}
	return strings.Join(nums, ".")
}

func (p *poll) securepointLicense() {
	lic, err := subtree(p.s, spLicense)
	if err != nil {
		p.optional("Lizenz", err)
		return
	}
	// in OID order: comLicLicensee, comLicEmail, comLicType, comLicUsersMax, comLicDaysLeft, comLicSerial, …
	if len(lic) < 5 {
		return
	}
	ck := model.Check{Key: "snmp:lic:UTM", Name: "Lizenz UTM", Group: "Lizenzen", State: model.StateOK,
		Facts: map[string]string{"Produkt": "Securepoint UTM"}}
	if s := toString(lic[0]); s != "" {
		ck.Facts["Lizenznehmer"] = s
	}
	if s := toString(lic[2]); s != "" {
		ck.Facts["Lizenztyp"] = s
		ck.Facts["Produkt"] = "Securepoint " + s
	}
	if n, ok := toFloat(lic[3]); ok && n > 0 {
		ck.Facts["Benutzer (max.)"] = fmtNum(n, 0)
	}
	days, ok := toFloat(lic[4])
	switch {
	case !ok:
		ck.State, ck.Message = model.StateUnknown, "Restlaufzeit nicht lesbar"
	case days >= licenseUnlimited:
		ck.Message = "unbefristet"
	case days <= 0:
		ck.State, ck.Message = model.StateCrit, "abgelaufen"
		ck.Metrics = []model.Metric{{Name: "license.days", Instance: "UTM", Value: 0, Unit: "d"}}
	default:
		until := p.now.AddDate(0, 0, int(days))
		ck.Facts["Läuft ab"] = until.Format("02.01.2006")
		ck.Message = "gültig bis " + until.Format("02.01.2006") + " (" + fmtNum(days, 0) + " Tage)"
		ck.Metrics = []model.Metric{{Name: "license.days", Instance: "UTM", Value: days, Unit: "d"}}
	}
	p.add(ck)
}

// ucdResources adds CPU and memory from UCD-SNMP-MIB (net-snmp) when HOST-RESOURCES-MIB
// provided none.
func (p *poll) ucdResources() {
	v, err := get(p.s, ucdCPUIdle, ucdLoad1, ucdMemTotal, ucdMemAvail, ucdMemBuffer, ucdMemCached)
	if err != nil {
		p.optional("CPU/RAM", err)
		return
	}
	if l, ok := parseNum(v.str(ucdLoad1)); ok {
		p.facts["Last"] = fmtNum(l, 2)
	}
	if idle, ok := v.num(ucdCPUIdle); ok && idle >= 0 && idle <= 100 && !p.has("snmp:cpu") {
		p.add(cpuCheck(100-idle, "Auslastung "+fmtPct(100-idle)))
	}
	total, okT := v.num(ucdMemTotal)
	avail, okA := v.num(ucdMemAvail)
	if okT && okA && total > 0 && avail >= 0 && avail <= total && !p.has("snmp:mem") {
		buf, _ := v.num(ucdMemBuffer)
		cached, _ := v.num(ucdMemCached)
		used := total - avail
		if used-buf-cached > 0 {
			used -= buf + cached
		}
		p.add(memCheck(used*1024, total*1024))
	}
}

// securepointCluster reads clusterStatusEnabled, -Priority, -State, -SyncState and
// -Maintainance; the text values are recognised by content.
func (p *poll) securepointCluster() {
	pdus, err := subtree(p.s, spCluster)
	if err != nil {
		p.optional("Cluster", err)
		return
	}
	enabled := -1
	var prio, state, sync, maint string
	for _, pd := range pdus {
		if pd.Type != gosnmp.OctetString {
			if n, ok := toInt(pd); ok && enabled < 0 {
				enabled = n
			}
			continue
		}
		switch s := strings.ToLower(toString(pd)); s {
		case "high", "low":
			prio = s
		case "master", "backup", "spare", "slave":
			state = s
		case "synchronized", "pending", "error":
			sync = s
		case "true", "false":
			maint = s
		}
	}
	if enabled != 1 {
		return
	}
	words := map[string]string{"master": "Master", "backup": "Backup", "spare": "Spare", "slave": "Backup",
		"synchronized": "synchronisiert", "pending": "Synchronisierung ausstehend", "error": "Synchronisierungsfehler",
		"high": "hoch", "low": "niedrig"}
	ck := model.Check{Key: "snmp:cluster", Name: "Hochverfügbarkeit (Cluster)", Group: "Hochverfügbarkeit", State: model.StateOK, Facts: map[string]string{}}
	if state != "" {
		ck.Facts["Rolle"] = words[state]
	}
	if prio != "" {
		ck.Facts["Priorität"] = words[prio]
	}
	if sync != "" {
		ck.Facts["Synchronisierung"] = words[sync]
	}
	if maint != "" {
		ck.Facts["Wartungsmodus"] = map[string]string{"true": "ja", "false": "nein"}[maint]
	}
	parts := []string{firstOr(words[state], "Rolle unbekannt")}
	switch sync {
	case "error":
		ck.State = model.StateCrit
		parts = append([]string{"Synchronisierung fehlerhaft"}, parts...)
	case "pending":
		ck.State = model.StateWarn
		parts = append(parts, "Synchronisierung ausstehend")
	case "synchronized":
		parts = append(parts, "synchronisiert")
	}
	if maint == "true" {
		ck.State = model.Worst(ck.State, model.StateWarn)
		parts = append(parts, "Wartungsmodus")
	}
	ck.Message = strings.Join(parts, " · ")
	p.add(ck)
}

func firstOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (p *poll) securepointIPsec() {
	t, err := walkRows(p.s, spIPsec)
	if err != nil {
		p.optional("IPsec", err)
		return
	}
	for _, r := range t.rows {
		name := t.str(spIPsecName, r)
		if name == "" {
			continue
		}
		ck := model.Check{Key: "snmp:vpn:" + name, Name: "VPN " + name, Group: "VPN", State: model.StateOK}
		switch st, _ := t.int(spIPsecStatus, r); st {
		case 1:
			ck.Message = "verbunden"
		case 2:
			ck.State, ck.Message = model.StateCrit, "getrennt"
		default:
			ck.State, ck.Message = model.StateUnknown, "Status unbekannt"
		}
		p.add(ck)
	}
	// subnet pairs of the connections (ipsecSubnetStatus "up"/"down", since 12.6.2)
	ct, err := walkRows(p.s, spIPsecConn)
	if err != nil || len(ct.rows) == 0 {
		return
	}
	var down []string
	n := 0
	for _, r := range ct.rows {
		st := strings.ToLower(ct.str(spSubnetStatus, r))
		if st == "" {
			continue
		}
		n++
		if st == "up" {
			continue
		}
		label := ""
		for _, s := range ct.texts(r) {
			if ls := strings.ToLower(s); ls != "up" && ls != "down" && s != "" {
				label = s
				break
			}
		}
		down = append(down, firstOr(label, "Verbindung "+r))
	}
	if n == 0 {
		return
	}
	ck := model.Check{Key: "snmp:ipsec-subnets", Name: "IPsec-Subnetze", Group: "VPN", State: model.StateOK,
		Message: strconv.Itoa(n-len(down)) + " von " + strconv.Itoa(n) + " Subnetzpaaren verbunden"}
	if len(down) > 0 {
		ck.State = model.StateCrit
		ck.Message = "getrennt: " + strings.Join(down, ", ") + " · " + ck.Message
	}
	p.add(ck)
}

var wgKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{42,43}=$`)

// securepointWireGuard reads the peers: the connection name, the endpoint and the last
// handshake (a Unix timestamp) are recognised by content.
func (p *poll) securepointWireGuard() {
	t, err := walkRows(p.s, spWGPeer)
	if err != nil {
		p.optional("WireGuard", err)
		return
	}
	cols := t.sortedCols()
	for _, r := range t.rows {
		var name, endpoint string
		var hs float64
		hasHS := false
		for _, c := range cols {
			pd, ok := t.cols[c][r]
			if !ok {
				continue
			}
			if pd.Type != gosnmp.OctetString {
				if v, ok := toFloat(pd); ok && v > 1e9 && v < 1e10 && !hasHS {
					hs, hasHS = v, true
				}
				continue
			}
			s := toString(pd)
			switch {
			case s == "" || wgKeyRe.MatchString(s):
			case isNumeric(s):
				if v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); v > 1e9 && v < 1e10 && !hasHS {
					hs, hasHS = v, true
				}
			case endpoint == "" && strings.Contains(s, ":") && !strings.Contains(s, " "):
				endpoint = s
			case name == "":
				name = s
			}
		}
		if name == "" {
			name = "Peer " + r
		}
		ck := model.Check{Key: "snmp:wg:" + name, Name: "WireGuard " + name, Group: "VPN", State: model.StateOK, Facts: map[string]string{}}
		if endpoint != "" {
			ck.Facts["Gegenstelle"] = endpoint
		}
		if !hasHS {
			ck.Message = "noch nie verbunden"
			p.add(ck)
			continue
		}
		at := time.Unix(int64(hs), 0)
		age := max(p.now.Sub(at), 0)
		ck.Facts["Letzter Handshake"] = at.Local().Format("02.01.2006 15:04:05")
		if age > wgStale {
			ck.State, ck.Message = model.StateWarn, "keine Verbindung · letzter Handshake vor "+fmtAge(age)
		} else {
			ck.Message = "verbunden · Handshake vor " + fmtAge(age)
		}
		p.add(ck)
	}
}

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

func fmtAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tagen"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	case d >= time.Minute:
		return strconv.Itoa(int(d.Minutes())) + " Min."
	}
	return strconv.Itoa(int(d.Seconds())) + " s"
}

// securepointServices reads the virus pattern updates (snmp:av) and the application status
// table (snmp:services). A virus scanner that never loaded a pattern (no last update, no last
// run) is not licensed or not in use: a UTM without an Ikarus license reports ikarus as
// error(4) with a failed pattern update, which is no problem to alert on.
func (p *poll) securepointServices() {
	unused := map[string]bool{}
	if av, err := walkRows(p.s, spAVPattern); err != nil {
		p.optional("Virenmuster", err)
	} else {
		ck := model.Check{Key: "snmp:av", Name: "Virenmuster", Group: "Dienste", State: model.StateOK, Facts: map[string]string{}}
		var failed []string
		for _, r := range av.rows {
			prod := firstOr(av.str(spAVProduct, r), "Virenscanner "+r)
			updated, ran := av.str(spAVUpdate, r), av.str(spAVRun, r)
			if updated == "" && ran == "" {
				unused[prod] = true
				continue
			}
			if st, _ := av.int(spAVState, r); st == 2 {
				failed = append(failed, prod)
				ck.Facts[prod] = "Update fehlgeschlagen"
			} else {
				ck.Facts[prod] = "aktuell"
			}
			if updated != "" {
				ck.Facts[prod] += " · Stand " + updated
			}
		}
		if len(ck.Facts) > 0 {
			ck.Message = "Updates in Ordnung"
			if len(failed) > 0 {
				ck.State, ck.Message = model.StateWarn, "Update fehlgeschlagen: "+strings.Join(failed, ", ")
			}
			p.add(ck)
		}
	}
	t, err := walkRows(p.s, spApps)
	if err != nil {
		p.optional("Dienste", err)
		return
	}
	if len(t.rows) == 0 {
		return
	}
	// only running and failed services are listed: a UTM has some 25, most of them stopped
	// because they are not set up
	ck := model.Check{Key: "snmp:services", Name: "UTM-Dienste", Group: "Dienste", State: model.StateOK, Facts: map[string]string{}}
	var bad []string
	running := 0
	for _, r := range t.rows {
		name := t.str(spAppName, r)
		st, ok := t.int(spAppStatus, r)
		if name == "" || !ok {
			continue
		}
		switch {
		case st == 1:
			running++
			ck.Facts[name] = spAppStates[st]
		case st == 4 && unused[name]:
			ck.Facts[name] = "nicht lizenziert"
		case st == 4:
			bad = append(bad, name)
			ck.Facts[name] = spAppStates[st]
		case st != 2 && st != 9:
			ck.Facts[name] = "Status " + strconv.Itoa(st)
		}
	}
	ck.Message = strconv.Itoa(running) + " Dienste laufen"
	if len(bad) > 0 {
		ck.State = model.StateWarn
		ck.Message = "Fehler: " + strings.Join(bad, ", ") + " · " + ck.Message
	}
	ck.Metrics = []model.Metric{{Name: "services.failed", Value: float64(len(bad))}}
	p.add(ck)
}

// securepointConntrack reports the connection tracking table (nf_conntrack max and count).
func (p *poll) securepointConntrack() {
	pdus, err := subtree(p.s, spConntrack)
	if err != nil {
		p.optional("Verbindungstabelle", err)
		return
	}
	var maxN, count float64
	var okM, okC bool
	for _, pd := range pdus {
		name := trimDot(pd.Name)
		v, ok := toFloat(pd)
		if !ok {
			continue
		}
		switch {
		case name == spConntrack+".1" || strings.HasPrefix(name, spConntrack+".1."):
			maxN, okM = v, true
		case name == spConntrack+".2" || strings.HasPrefix(name, spConntrack+".2."):
			count, okC = v, true
		}
	}
	if !okM || !okC || maxN <= 0 {
		return
	}
	pct := count / maxN * 100
	p.add(model.Check{Key: "snmp:conntrack", Name: "Verbindungstabelle", Group: "System", State: model.StateOK,
		Message: fmtNum(count, 0) + " von " + fmtNum(maxN, 0) + " Verbindungen (" + fmtPct(pct) + ")",
		Metrics: []model.Metric{{Name: "conntrack.usage", Value: math.Round(pct*10) / 10, Unit: "%"}, {Name: "conntrack.count", Value: count}}})
}
