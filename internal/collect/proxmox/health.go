package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// Hardware and cluster health beyond /disks/list and /disks/zfs: SMART attributes, ZFS pool
// details (vdev errors, scrub, resilver), storage replication, HA, Ceph and the node
// certificates.

const (
	smartTTL = 30 * time.Minute // smartctl may wake sleeping disks: ask rarely
	certTTL  = 6 * time.Hour
)

// ---- API types ----

// text decodes a JSON string or number as text (SMART raw values come as either).
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = text(s)
		return nil
	}
	*t = text(strings.TrimSpace(string(b)))
	return nil
}

// smartInfo is GET /nodes/{node}/disks/smart?disk=/dev/…
type smartInfo struct {
	Health     string      `json:"health"`
	Type       string      `json:"type"` // "ata" with attributes, "text" (NVMe, SAS) with smartctl output
	Attributes []smartAttr `json:"attributes"`
	Text       string      `json:"text"`
}

type smartAttr struct {
	ID        num    `json:"id"` // PVE sends "  5"
	Name      string `json:"name"`
	Value     num    `json:"value"`
	Threshold num    `json:"threshold"`
	Raw       text   `json:"raw"`
	Fail      string `json:"fail"` // "-", "FAILING_NOW", "In_the_past"
}

// zfsDetail is GET /nodes/{node}/disks/zfs/{name} (zpool status).
type zfsDetail struct {
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Status   string    `json:"status"`
	Action   string    `json:"action"`
	Scan     string    `json:"scan"`
	Errors   string    `json:"errors"`
	Children []zfsVdev `json:"children"`
}

type zfsVdev struct {
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Read     errNum    `json:"read"`
	Write    errNum    `json:"write"`
	Cksum    errNum    `json:"cksum"`
	Msg      string    `json:"msg"`
	Children []zfsVdev `json:"children"`
}

// errNum is a zpool error counter: a number or text like "1.20K".
type errNum float64

func (n *errNum) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1e9, strings.TrimSuffix(s, "G")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		f = 0
	}
	*n = errNum(f * mult)
	return nil
}

// replJob is an entry of GET /nodes/{node}/replication (jobs whose source is this node).
type replJob struct {
	ID        string `json:"id"` // "100-0"
	Guest     num    `json:"guest"`
	Target    string `json:"target"`
	LastSync  num    `json:"last_sync"`
	Duration  num    `json:"duration"`
	FailCount num    `json:"fail_count"`
	Error     string `json:"error"`
	Disable   num    `json:"disable"`
	Schedule  string `json:"schedule"`
}

// haEntry is an entry of GET /cluster/ha/status/current.
type haEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // quorum, master, lrm, service
	Status  string `json:"status"`
	SID     string `json:"sid"`
	State   string `json:"state"`
	Node    string `json:"node"`
	Quorate num    `json:"quorate"`
}

// cephStatus is GET /cluster/ceph/status (the parts used).
type cephStatus struct {
	Health struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Severity string `json:"severity"`
			Summary  struct {
				Message string `json:"message"`
			} `json:"summary"`
		} `json:"checks"`
	} `json:"health"`
	PGMap struct {
		BytesUsed  num `json:"bytes_used"`
		BytesTotal num `json:"bytes_total"`
	} `json:"pgmap"`
}

// certInfo is an entry of GET /nodes/{node}/certificates/info.
type certInfo struct {
	Filename string   `json:"filename"`
	NotAfter num      `json:"notafter"`
	Subject  string   `json:"subject"`
	Issuer   string   `json:"issuer"`
	SAN      []string `json:"san"`
}

func (c *client) smart(ctx context.Context, node, devPath string) (smartInfo, error) {
	var out smartInfo
	err := c.get(ctx, nodePath(node)+"/disks/smart", url.Values{"disk": {devPath}}, &out)
	return out, err
}

func (c *client) zfsDetail(ctx context.Context, node, pool string) (zfsDetail, error) {
	var out zfsDetail
	err := c.get(ctx, nodePath(node)+"/disks/zfs/"+url.PathEscape(pool), nil, &out)
	return out, err
}

func (c *client) replication(ctx context.Context, node string) ([]replJob, error) {
	var out []replJob
	err := c.get(ctx, nodePath(node)+"/replication", nil, &out)
	return out, err
}

func (c *client) haStatus(ctx context.Context) ([]haEntry, error) {
	var out []haEntry
	err := c.get(ctx, "/cluster/ha/status/current", nil, &out)
	return out, err
}

func (c *client) ceph(ctx context.Context) (cephStatus, error) {
	var out cephStatus
	err := c.get(ctx, "/cluster/ceph/status", nil, &out)
	return out, err
}

func (c *client) certificates(ctx context.Context, node string) ([]certInfo, error) {
	var out []certInfo
	err := c.get(ctx, nodePath(node)+"/certificates/info", nil, &out)
	return out, err
}

// ---- cached per-node calls ----

type smartEntry struct {
	info smartInfo
	err  error
	at   time.Time
}

type certEntry struct {
	list []certInfo
	err  error
	at   time.Time
}

// smartFor returns the SMART data of a disk, cached for smartTTL (errors too).
func (c *Collector) smartFor(ctx context.Context, cl *client, devID, node, devPath string, now time.Time) smartEntry {
	key := node + "\x00" + devPath
	c.mu.Lock()
	e, ok := c.dev(devID).smart[key]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < smartTTL {
		return e
	}
	info, err := cl.smart(ctx, node, devPath)
	if ctx.Err() != nil {
		return e // poll cancelled: keep the old entry (if any)
	}
	e = smartEntry{info: info, err: err, at: now}
	c.mu.Lock()
	c.dev(devID).smart[key] = e
	c.mu.Unlock()
	return e
}

// certsFor returns the certificates of a node, cached for certTTL.
func (c *Collector) certsFor(ctx context.Context, cl *client, devID, node string, now time.Time) certEntry {
	c.mu.Lock()
	e, ok := c.dev(devID).certs[node]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < certTTL {
		return e
	}
	list, err := cl.certificates(ctx, node)
	if ctx.Err() != nil {
		return e
	}
	e = certEntry{list: list, err: err, at: now}
	c.mu.Lock()
	c.dev(devID).certs[node] = e
	c.mu.Unlock()
	return e
}

// ---- SMART ----

// smartValues are the SMART values of a disk.
type smartValues struct {
	v       map[string]float64 // metric name -> value
	facts   map[string]string
	failing []string // attributes at or below their threshold now
	critMsg []string // NVMe critical warning bits
}

func newSmartValues() smartValues {
	return smartValues{v: map[string]float64{}, facts: map[string]string{}}
}

var leadingNum = regexp.MustCompile(`^\s*([0-9][0-9,.]*)`)

// rawNumber is the leading number of a SMART raw value ("36 (Min/Max 22/45)", "12345h+12m",
// "4,321"); commas are thousands separators.
func rawNumber(s string) (float64, bool) {
	m := leadingNum.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	return f, err == nil
}

// ataSmart reads the attributes of an ATA disk.
func ataSmart(attrs []smartAttr) smartValues {
	sv := newSmartValues()
	byID := map[int]smartAttr{}
	for _, a := range attrs {
		byID[int(a.ID)] = a
		if strings.EqualFold(strings.TrimSpace(a.Fail), "FAILING_NOW") {
			sv.failing = append(sv.failing, strings.ReplaceAll(a.Name, "_", " "))
		}
	}
	raw := func(id int) (float64, bool) {
		a, ok := byID[id]
		if !ok {
			return 0, false
		}
		return rawNumber(string(a.Raw))
	}
	sum := func(name string, ids ...int) {
		for _, id := range ids {
			if v, ok := raw(id); ok {
				sv.v[name] += v
			}
		}
	}
	if v, ok := raw(194); ok && v > 0 && v < 150 {
		sv.v["disk.temp"] = v
	} else if v, ok := raw(190); ok && v > 0 && v < 150 {
		sv.v["disk.temp"] = v
	}
	sum("disk.reallocated", 5)
	sum("disk.pending", 197)
	sum("disk.uncorrectable", 198, 187)
	sum("disk.crc_errors", 199)
	if h, ok := raw(9); ok {
		sv.facts["Betriebsstunden"] = hoursText(h)
	}
	if n, ok := raw(12); ok {
		sv.facts["Einschaltvorgänge"] = groupThousands(int64(n))
	}
	return sv
}

// nvmeLine matches "Key:   value" lines of smartctl's NVMe health log.
var nvmeLine = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z /]+?):\s+(.+?)\s*$`)

// nvmeSmart reads the smartctl text of an NVMe disk.
func nvmeSmart(txt string) smartValues {
	sv := newSmartValues()
	kv := map[string]string{}
	for line := range strings.Lines(txt) {
		if m := nvmeLine.FindStringSubmatch(line); m != nil {
			kv[strings.ToLower(m[1])] = m[2]
		}
	}
	val := func(k string) (float64, bool) { return rawNumber(strings.TrimSuffix(kv[k], "%")) }
	if v, ok := val("temperature"); ok && v > 0 && v < 150 {
		sv.v["disk.temp"] = v
	}
	if v, ok := val("available spare"); ok {
		sv.v["disk.spare"] = v
	}
	if v, ok := val("percentage used"); ok {
		sv.v["disk.wearout"] = v
	}
	if v, ok := val("media and data integrity errors"); ok {
		sv.v["disk.media_errors"] = v
	}
	if v, ok := val("power on hours"); ok {
		sv.facts["Betriebsstunden"] = hoursText(v)
	}
	if v, ok := val("unsafe shutdowns"); ok {
		sv.facts["Unsichere Abschaltungen"] = groupThousands(int64(v))
	}
	if w := kv["data units written"]; w != "" {
		if i := strings.Index(w, "["); i >= 0 {
			sv.facts["Geschrieben"] = strings.Trim(w[i:], "[]")
		}
	}
	if cw := strings.TrimSpace(kv["critical warning"]); cw != "" {
		if bits, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(cw), "0x"), 16, 8); err == nil && bits != 0 {
			for i, msg := range []string{"Reserve unter Grenzwert", "Temperatur außerhalb des Bereichs", "Zuverlässigkeit eingeschränkt", "nur noch lesbar", "Pufferspeicher ausgefallen"} {
				if bits&(1<<i) != 0 {
					sv.critMsg = append(sv.critMsg, msg)
				}
			}
		}
	}
	return sv
}

// hoursText formats power-on hours ("23.456 h (2,7 Jahre)").
func hoursText(h float64) string {
	s := groupThousands(int64(h)) + " h"
	if y := h / 8766; y >= 1 {
		s += " (" + strings.Replace(fmt.Sprintf("%.1f", y), ".", ",", 1) + " Jahre)"
	} else if d := h / 24; d >= 1 {
		s += " (" + strconv.Itoa(int(d)) + " Tage)"
	}
	return s
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

// smartProblems names the SMART counters that are not zero (the thresholds decide the state).
func smartProblems(v map[string]float64) []string {
	var out []string
	add := func(name, one, many string) {
		if n := int(v[name]); n == 1 {
			out = append(out, one)
		} else if n > 1 {
			out = append(out, groupThousands(int64(n))+" "+many)
		}
	}
	add("disk.pending", "1 ausstehender Sektor", "ausstehende Sektoren")
	add("disk.reallocated", "1 neu zugewiesener Sektor", "neu zugewiesene Sektoren")
	add("disk.uncorrectable", "1 nicht korrigierbarer Fehler", "nicht korrigierbare Fehler")
	add("disk.media_errors", "1 Medienfehler", "Medienfehler")
	return out
}

// ---- ZFS ----

// scanInfo is the parsed "scan" line of zpool status.
type scanInfo struct {
	kind    string // scrub, resilver, "" (none requested or canceled)
	running bool
	done    float64 // percent while running
	toGo    string  // "01:23:45" while running
	at      time.Time
	errors  int
}

var (
	scanDate   = regexp.MustCompile(`(?:on|since) ((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) \w{3}\s+\d{1,2} \d{2}:\d{2}:\d{2} \d{4})`)
	scanErrors = regexp.MustCompile(`with (\d+) errors`)
	scanDone   = regexp.MustCompile(`([0-9.]+)% done(?:, ([0-9:]+|no estimated completion time) to go)?`)
)

func parseScan(s string, loc *time.Location) scanInfo {
	var si scanInfo
	low := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(low, "canceled"):
		return si
	case strings.HasPrefix(low, "scrub"):
		si.kind = "scrub"
	case strings.HasPrefix(low, "resilver"):
		si.kind = "resilver"
	default:
		return si
	}
	si.running = strings.Contains(low, "in progress")
	if m := scanDate.FindStringSubmatch(s); m != nil {
		if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(m[1]), " "), loc); err == nil {
			si.at = t
		}
	}
	if m := scanErrors.FindStringSubmatch(s); m != nil {
		si.errors, _ = strconv.Atoi(m[1])
	}
	if m := scanDone.FindStringSubmatch(s); m != nil {
		si.done, _ = strconv.ParseFloat(m[1], 64)
		if m[2] != "" && !strings.HasPrefix(m[2], "no") {
			si.toGo = m[2]
		}
	}
	return si
}

// vdevIssues sums the error counters of all vdevs and lists those not ONLINE.
func vdevIssues(list []zfsVdev) (errs float64, bad []string) {
	var walk func([]zfsVdev)
	walk = func(vs []zfsVdev) {
		for _, v := range vs {
			errs += float64(v.Read + v.Write + v.Cksum)
			st := strings.ToUpper(strings.TrimSpace(v.State))
			if st != "" && st != "ONLINE" && st != "AVAIL" && st != "INUSE" {
				bad = append(bad, v.Name+" "+st)
			}
			walk(v.Children)
		}
	}
	walk(list)
	return errs, bad
}

// applyZFSDetail adds the zpool status details to a pool check.
func applyZFSDetail(ch *model.Check, inst string, d zfsDetail, now time.Time) {
	errs, bad := vdevIssues(d.Children)
	ch.Metrics = append(ch.Metrics, model.Metric{Name: "zfs.errors", Instance: inst, Value: errs})
	var msg []string
	if len(bad) > 0 {
		msg = append(msg, strings.Join(bad, ", "))
	}
	if errs > 0 {
		msg = append(msg, fmt.Sprintf("%s Lese-/Schreib-/Prüfsummenfehler", groupThousands(int64(errs))))
	}
	if e := strings.TrimSpace(d.Errors); e != "" && !strings.EqualFold(e, "No known data errors") {
		ch.State = model.Worst(ch.State, model.StateCrit)
		msg = append(msg, "Datenfehler: "+truncate(e, 120))
		ch.Facts["Datenfehler"] = e
	}
	si := parseScan(d.Scan, now.Location())
	switch {
	case si.kind == "resilver" && si.running:
		ch.State = model.Worst(ch.State, model.StateWarn)
		msg = append(msg, "Resilver läuft"+progressText(si))
	case si.kind == "scrub" && si.running:
		msg = append(msg, "Scrub läuft"+progressText(si))
	}
	if si.kind == "scrub" && !si.running && !si.at.IsZero() {
		days := now.Sub(si.at).Hours() / 24
		ch.Metrics = append(ch.Metrics, model.Metric{Name: "zfs.scrub_age", Instance: inst, Value: round2(max(days, 0)), Unit: "d"})
		ch.Facts["Letzter Scrub"] = si.at.Format("02.01.2006 15:04")
		if si.errors > 0 {
			ch.State = model.Worst(ch.State, model.StateWarn)
			msg = append(msg, fmt.Sprintf("letzter Scrub mit %d Fehlern", si.errors))
		}
	} else if si.kind == "" && strings.Contains(strings.ToLower(d.Scan), "none requested") {
		ch.Facts["Letzter Scrub"] = "noch nie"
	}
	if s := strings.TrimSpace(d.Scan); s != "" {
		ch.Facts["Prüfung"] = strings.Join(strings.Fields(s), " ")
	}
	if s := strings.TrimSpace(d.Status); s != "" {
		ch.Facts["Hinweis"] = strings.Join(strings.Fields(s), " ")
	}
	if len(msg) > 0 {
		ch.Message += " · " + strings.Join(msg, " · ")
	}
}

func progressText(si scanInfo) string {
	if si.done <= 0 {
		return ""
	}
	s := ": " + strconv.Itoa(int(si.done)) + " %"
	if si.toGo != "" {
		s += ", noch " + si.toGo
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- replication, HA, Ceph, certificates ----

func replicationChecks(node string, jobs []replJob, names map[string]string, now time.Time) []model.Check {
	var out []model.Check
	slices.SortFunc(jobs, func(a, b replJob) int { return strings.Compare(a.ID, b.ID) })
	for _, j := range jobs {
		if j.ID == "" {
			continue
		}
		guest := strconv.Itoa(int(j.Guest))
		label := guest
		if n := names[guest]; n != "" {
			label += " " + n
		}
		ch := model.Check{Key: "pve:repl:" + j.ID, Name: "Replikation " + label + " → " + j.Target, Group: "Replikation",
			Facts: map[string]string{"Job": j.ID, "Quelle": node, "Ziel": j.Target}}
		if j.Schedule != "" {
			ch.Facts["Zeitplan"] = j.Schedule
		}
		ago := now.Sub(time.Unix(int64(j.LastSync), 0))
		switch {
		case j.Disable != 0:
			ch.State, ch.Message = model.StateOK, "deaktiviert"
		case j.FailCount > 0:
			ch.State = model.StateCrit
			ch.Message = fmt.Sprintf("fehlgeschlagen (%d×)", int(j.FailCount))
			if e := strings.TrimSpace(j.Error); e != "" {
				ch.Message += ": " + truncate(e, 160)
			}
		case j.LastSync <= 0:
			ch.State, ch.Message = model.StateUnknown, "noch nicht gelaufen"
		default:
			ch.State = model.StateOK
			ch.Message = "zuletzt vor " + fmtDuration(max(ago.Seconds(), 0))
			if ago < time.Minute {
				ch.Message = "zuletzt gerade eben"
			}
			if j.Duration >= 60 {
				ch.Message += ", Dauer " + fmtDuration(float64(j.Duration))
			} else if j.Duration > 0 {
				ch.Message += fmt.Sprintf(", Dauer %d s", max(int(j.Duration+0.5), 1))
			}
		}
		if j.LastSync > 0 && j.Disable == 0 {
			ch.Metrics = []model.Metric{{Name: "repl.age", Instance: j.ID, Value: round2(max(ago.Minutes(), 0)), Unit: "min"}}
			ch.Facts["Letzte Synchronisierung"] = fmtTime(float64(j.LastSync))
		}
		out = append(out, ch)
	}
	return out
}

// haCheck summarizes the HA resources; false when HA is not used.
func haCheck(list []haEntry) (model.Check, bool) {
	ch := model.Check{Key: "pve:ha", Name: "Hochverfügbarkeit", Group: "Cluster", State: model.StateOK, Facts: map[string]string{}}
	var services, bad, warn []string
	quorumOK := true
	for _, e := range list {
		switch e.Type {
		case "quorum":
			quorumOK = strings.EqualFold(e.Status, "OK") || e.Quorate != 0
		case "master":
			ch.Facts["Manager"] = e.Status
		case "service":
			services = append(services, e.SID)
			switch e.State {
			case "error":
				bad = append(bad, e.SID+" Fehler")
			case "fence":
				warn = append(warn, e.SID+" wird abgegrenzt")
			case "recovery":
				warn = append(warn, e.SID+" wird wiederhergestellt")
			}
		}
	}
	if len(services) == 0 {
		return ch, false
	}
	ch.Facts["Ressourcen"] = strings.Join(services, ", ")
	switch {
	case !quorumOK:
		ch.State, ch.Message = model.StateCrit, "kein Quorum"
	case len(bad) > 0:
		ch.State, ch.Message = model.StateCrit, strings.Join(append(bad, warn...), ", ")
	case len(warn) > 0:
		ch.State, ch.Message = model.StateWarn, strings.Join(warn, ", ")
	case len(services) == 1:
		ch.Message = "1 Ressource, in Ordnung"
	default:
		ch.Message = fmt.Sprintf("%d Ressourcen, alle in Ordnung", len(services))
	}
	return ch, true
}

func cephCheck(s cephStatus) model.Check {
	ch := model.Check{Key: "pve:ceph", Name: "Ceph", Group: "Speicher"}
	switch strings.ToUpper(s.Health.Status) {
	case "HEALTH_OK":
		ch.State = model.StateOK
	case "HEALTH_WARN":
		ch.State = model.StateWarn
	case "HEALTH_ERR":
		ch.State = model.StateCrit
	default:
		ch.State = model.StateUnknown
	}
	keys := make([]string, 0, len(s.Health.Checks))
	for k := range s.Health.Checks {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var msgs []string
	for _, k := range keys {
		if m := s.Health.Checks[k].Summary.Message; m != "" {
			msgs = append(msgs, m)
		}
	}
	ch.Message = s.Health.Status
	if len(msgs) > 0 {
		ch.Message += " · " + truncate(strings.Join(msgs, "; "), 200)
	}
	if s.PGMap.BytesTotal > 0 {
		p := pct(float64(s.PGMap.BytesUsed), float64(s.PGMap.BytesTotal))
		ch.Metrics = []model.Metric{{Name: "storage.usage", Instance: "ceph", Value: round2(p), Unit: "%"}}
		ch.Message += " · " + fmtPct(p) + " belegt"
	}
	return ch
}

// certCheck reports the certificate the node's web interface uses (pveproxy-ssl.pem when
// present, else pve-ssl.pem); false without one.
func certCheck(node string, list []certInfo, now time.Time) (model.Check, bool) {
	var pick *certInfo
	for _, name := range []string{"pveproxy-ssl.pem", "pve-ssl.pem"} {
		if i := slices.IndexFunc(list, func(c certInfo) bool { return c.Filename == name }); i >= 0 {
			pick = &list[i]
			break
		}
	}
	if pick == nil || pick.NotAfter <= 0 {
		return model.Check{}, false
	}
	until := time.Unix(int64(pick.NotAfter), 0)
	days := until.Sub(now).Hours() / 24
	ch := model.Check{Key: "pve:cert:" + node, Name: "Zertifikat " + node, Group: "Zertifikate", State: model.StateOK,
		Facts:   map[string]string{"Datei": pick.Filename, "Gültig bis": until.Format("02.01.2006")},
		Metrics: []model.Metric{{Name: "cert.days", Instance: node, Value: round2(days), Unit: "d"}}}
	if pick.Issuer != "" {
		ch.Facts["Aussteller"] = pick.Issuer
	}
	if len(pick.SAN) > 0 {
		ch.Facts["Namen"] = strings.Join(pick.SAN, ", ")
	}
	if days < 0 {
		ch.State, ch.Message = model.StateCrit, "abgelaufen am "+until.Format("02.01.2006")
	} else {
		ch.Message = fmt.Sprintf("gültig bis %s (%d Tage)", until.Format("02.01.2006"), int(days))
	}
	return ch, true
}
