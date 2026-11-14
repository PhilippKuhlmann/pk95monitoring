// Package pbs polls a Proxmox Backup Server over its REST API (HTTPS, port 8007) with an API
// token: the datastores (fill level and the server's own estimate when they are full), the
// verification state of the newest snapshot of every backup group, the age of the groups and
// the outcome of the verify, sync, prune and garbage collection tasks of the last 7 days.
package pbs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the port of the PBS API.
const DefaultPort = 8007

const (
	requestTimeout = 30 * time.Second
	snapshotTTL    = 30 * time.Minute // snapshot lists can be long: read twice an hour
	taskWindow     = 7 * 24 * time.Hour
	fullWarnDays   = 30 // a datastore estimated full within this many days is a warning
	maxNamespaces  = 20
)

// Collector polls Proxmox Backup Servers; it caches the snapshot lists per device.
type Collector struct {
	mu   sync.Mutex
	now  func() time.Time
	devs map[string]*devState
}

type devState struct {
	sig    string
	cl     *client
	snaps  map[string][]snapshot // datastore -> snapshots of all namespaces
	snapAt time.Time
}

// New returns a Collector.
func New() *Collector { return &Collector{now: time.Now, devs: map[string]*devState{}} }

// Forget drops the state of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.devs[deviceID]; st != nil {
		st.cl.http.CloseIdleConnections()
	}
	delete(c.devs, deviceID)
}

type client struct {
	base string
	auth string
	http *http.Client
}

func port(p int) int {
	if p <= 0 {
		return DefaultPort
	}
	return p
}

func normFingerprint(f string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(f)))
}

func newClient(host string, cfg model.PBSConfig) *client {
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if want := normFingerprint(cfg.Fingerprint); want != "" {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // verified below by pinning
		tr.TLSClientConfig.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("kein Zertifikat")
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != want {
				return errors.New("Zertifikat passt nicht zum hinterlegten Fingerabdruck")
			}
			return nil
		}
	}
	return &client{base: "https://" + net.JoinHostPort(host, strconv.Itoa(port(cfg.Port))) + "/api2/json",
		auth: "PBSAPIToken=" + strings.TrimSpace(cfg.TokenID) + ":" + strings.TrimSpace(cfg.Secret),
		http: &http.Client{Transport: tr, Timeout: requestTimeout}}
}

func signature(host string, cfg model.PBSConfig) string {
	return strings.Join([]string{host, strconv.Itoa(port(cfg.Port)), cfg.TokenID, cfg.Secret, normFingerprint(cfg.Fingerprint)}, "\x00")
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "Token abgelehnt – Token-ID und Secret prüfen"
	case http.StatusForbidden:
		return "Keine Berechtigung – der Token braucht die Rolle Audit auf / (proxmox-backup-manager acl update / Audit --auth-id …)"
	}
	if e.msg != "" {
		return fmt.Sprintf("PBS antwortet mit HTTP %d: %s", e.status, e.msg)
	}
	return fmt.Sprintf("PBS antwortet mit HTTP %d", e.status)
}

func friendly(err error) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "Fingerabdruck"):
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck des Backup-Servers übernehmen (" + err.Error() + ")")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("Verbindung abgelehnt – läuft der Backup-Server (Port 8007)?")
	case strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "i/o timeout"):
		return errors.New("Zeitüberschreitung – Host und Port 8007 erreichbar (Firewall)?")
	}
	return err
}

// get calls GET path and decodes the "data" member of the answer.
func (cl *client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := cl.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", cl.auth)
	resp, err := cl.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		return &apiError{status: resp.StatusCode, msg: strings.TrimSpace(e.Message)}
	}
	var wrap struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return fmt.Errorf("Antwort von %s nicht lesbar: %w", path, err)
	}
	if out == nil || len(wrap.Data) == 0 || string(wrap.Data) == "null" {
		return nil
	}
	return json.Unmarshal(wrap.Data, out)
}

// num accepts JSON numbers and numeric strings.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = num(f)
	return nil
}

type version struct {
	Version string `json:"version"`
}

type dsUsage struct {
	Store    string `json:"store"`
	Total    num    `json:"total"`
	Used     num    `json:"used"`
	Avail    num    `json:"avail"`
	FullDate num    `json:"estimated-full-date"`
	Error    string `json:"error"`
}

type snapshot struct {
	NS           string `json:"-"`
	Type         string `json:"backup-type"`
	ID           string `json:"backup-id"`
	Time         num    `json:"backup-time"`
	Size         num    `json:"size"`
	Comment      string `json:"comment"`
	Verification *struct {
		State string `json:"state"`
	} `json:"verification"`
}

type task struct {
	UPID       string `json:"upid"`
	WorkerType string `json:"worker_type"`
	WorkerID   string `json:"worker_id"`
	End        num    `json:"endtime"`
	Status     string `json:"status"`
}

// Collect polls one backup server. It returns an error when the server cannot be reached or
// the token is rejected; datastores or tasks that cannot be read become check states.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.PBS == nil || strings.TrimSpace(d.PBS.TokenID) == "" {
		return nil, errors.New("PBS-Zugang fehlt (Token-ID und Secret)")
	}
	if strings.TrimSpace(d.Host) == "" {
		return nil, errors.New("Host fehlt")
	}
	st := c.state(d)
	var ver version
	if err := st.cl.get(ctx, "/version", nil, &ver); err != nil {
		return nil, err
	}
	now := c.now()
	checks := []model.Check{{Key: "pbs:server", Name: "Backup-Server", Group: "Server", State: model.StateOK,
		Message: "Proxmox Backup Server " + ver.Version, Facts: map[string]string{"Version": ver.Version}}}
	var usage []dsUsage
	if err := st.cl.get(ctx, "/status/datastore-usage", nil, &usage); err != nil {
		return nil, err
	}
	slices.SortFunc(usage, func(a, b dsUsage) int { return strings.Compare(a.Store, b.Store) })
	snaps := c.snapshots(ctx, d.ID, st, usage, now)
	for _, u := range usage {
		list, ok := snaps[u.Store]
		checks = append(checks, datastoreCheck(u, list, now))
		if ok {
			checks = append(checks, verifyCheck(u.Store, list))
			checks = append(checks, groupChecks(u.Store, list, now)...)
		}
	}
	var tasks []task
	q := url.Values{"since": {strconv.FormatInt(now.Add(-taskWindow).Unix(), 10)}, "limit": {"2000"}}
	if err := st.cl.get(ctx, "/nodes/localhost/tasks", q, &tasks); err != nil {
		checks = append(checks, model.Check{Key: "pbs:tasks", Name: "Aufgaben", Group: "Jobs", State: model.StateUnknown,
			Message: "Aufgaben nicht lesbar: " + err.Error()})
	} else {
		checks = append(checks, taskChecks(tasks, now)...)
	}
	return checks, nil
}

func (c *Collector) state(d model.Device) *devState {
	c.mu.Lock()
	defer c.mu.Unlock()
	sig := signature(d.Host, *d.PBS)
	st := c.devs[d.ID]
	if st == nil || st.sig != sig {
		if st != nil {
			st.cl.http.CloseIdleConnections()
		}
		st = &devState{sig: sig, cl: newClient(d.Host, *d.PBS)}
		c.devs[d.ID] = st
	}
	return st
}

// snapshots returns the snapshots of every datastore (all namespaces), cached for snapshotTTL.
// A datastore that cannot be listed is missing from the result.
func (c *Collector) snapshots(ctx context.Context, devID string, st *devState, usage []dsUsage, now time.Time) map[string][]snapshot {
	c.mu.Lock()
	cached, at := st.snaps, st.snapAt
	c.mu.Unlock()
	if cached != nil && now.Sub(at) < snapshotTTL {
		return cached
	}
	out := map[string][]snapshot{}
	for _, u := range usage {
		path := "/admin/datastore/" + url.PathEscape(u.Store)
		namespaces := []string{""}
		var nsList []struct {
			NS string `json:"ns"`
		}
		if err := st.cl.get(ctx, path+"/namespace", nil, &nsList); err == nil && len(nsList) > 0 {
			namespaces = namespaces[:0]
			for _, n := range nsList {
				if len(namespaces) < maxNamespaces {
					namespaces = append(namespaces, n.NS)
				}
			}
		}
		var all []snapshot
		ok := true
		for _, ns := range namespaces {
			var list []snapshot
			q := url.Values{}
			if ns != "" {
				q.Set("ns", ns)
			}
			if err := st.cl.get(ctx, path+"/snapshots", q, &list); err != nil {
				ok = false
				break
			}
			for i := range list {
				list[i].NS = ns
			}
			all = append(all, list...)
		}
		if ok {
			out[u.Store] = all
		}
	}
	c.mu.Lock()
	if c.devs[devID] == st {
		st.snaps, st.snapAt = out, now
	}
	c.mu.Unlock()
	return out
}

func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", b, units[i]), ".", ",", 1)
}

func fmtTime(unix float64) string {
	return time.Unix(int64(unix), 0).Local().Format("02.01.2006 15:04")
}

func fmtAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tagen"
	case d >= 24*time.Hour:
		return "1 Tag"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	}
	return strconv.Itoa(int(d.Minutes())) + " Min."
}

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// datastoreCheck is "pbs:ds:<store>": fill level (storage.*) and the estimate when it is full.
func datastoreCheck(u dsUsage, snaps []snapshot, now time.Time) model.Check {
	ch := model.Check{Key: "pbs:ds:" + u.Store, Name: "Datastore " + u.Store, Group: "Datastores", State: model.StateOK, Facts: map[string]string{}}
	if u.Error != "" {
		ch.State, ch.Message = model.StateUnknown, "Datastore nicht lesbar: "+u.Error
		return ch
	}
	if u.Total <= 0 {
		ch.State, ch.Message = model.StateUnknown, "Größe unbekannt (Datastore nicht eingehängt?)"
		return ch
	}
	usage := 100 * float64(u.Used) / float64(u.Total)
	ch.Metrics = []model.Metric{{Name: "storage.usage", Instance: u.Store, Value: round2(usage), Unit: "%"},
		{Name: "storage.used", Instance: u.Store, Value: float64(u.Used), Unit: "B"}, {Name: "storage.total", Instance: u.Store, Value: float64(u.Total), Unit: "B"}}
	ch.Message = fmt.Sprintf("%.0f %% belegt · %s frei von %s", usage, fmtBytes(float64(u.Avail)), fmtBytes(float64(u.Total)))
	if snaps != nil {
		groups := map[string]bool{}
		for _, s := range snaps {
			groups[s.NS+"/"+s.Type+"/"+s.ID] = true
		}
		ch.Facts["Sicherungsgruppen"] = strconv.Itoa(len(groups))
		ch.Facts["Sicherungen"] = strconv.Itoa(len(snaps))
	}
	if u.FullDate > 0 {
		full := time.Unix(int64(u.FullDate), 0)
		days := int(full.Sub(now).Hours() / 24)
		ch.Facts["Voll voraussichtlich"] = full.Local().Format("02.01.2006")
		if days < fullWarnDays {
			ch.State = model.StateWarn
			ch.Message += fmt.Sprintf(" · voll voraussichtlich in %d Tagen (%s)", max(0, days), full.Local().Format("02.01.2006"))
		}
	}
	return ch
}

// group is the newest snapshot of a backup group and how many it has.
type group struct {
	newest snapshot
	count  int
}

func groupsOf(snaps []snapshot) []group {
	byKey := map[string]*group{}
	var keys []string
	for _, s := range snaps {
		k := s.NS + "\x00" + s.Type + "/" + s.ID
		g := byKey[k]
		if g == nil {
			g = &group{newest: s}
			byKey[k] = g
			keys = append(keys, k)
		}
		g.count++
		if s.Time > g.newest.Time {
			g.newest = s
		}
	}
	out := make([]group, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	slices.SortFunc(out, func(a, b group) int {
		return cmp.Or(strings.Compare(a.newest.NS, b.newest.NS), strings.Compare(a.newest.Type, b.newest.Type), natCmp(a.newest.ID, b.newest.ID))
	})
	return out
}

// natCmp compares numeric IDs by value ("100" before "1000").
func natCmp(a, b string) int {
	if x, err1 := strconv.Atoi(a); err1 == nil {
		if y, err2 := strconv.Atoi(b); err2 == nil {
			return cmp.Compare(x, y)
		}
	}
	return strings.Compare(a, b)
}

func groupLabel(s snapshot) string {
	l := s.Type + "/" + s.ID
	if s.NS != "" {
		l = s.NS + ": " + l
	}
	return l
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// verifyCheck is "pbs:verify:<store>": the newest snapshot of a group whose verification
// failed is critical (that backup may not be restorable).
func verifyCheck(store string, snaps []snapshot) model.Check {
	ch := model.Check{Key: "pbs:verify:" + store, Name: "Verifizierung " + store, Group: "Verifizierung", State: model.StateOK, Facts: map[string]string{}}
	var failed []string
	unverified := 0
	groups := groupsOf(snaps)
	for _, g := range groups {
		switch {
		case g.newest.Verification == nil:
			unverified++
		case g.newest.Verification.State == "failed":
			failed = append(failed, groupLabel(g.newest))
		}
	}
	ch.Facts["Gruppen"] = strconv.Itoa(len(groups))
	ch.Facts["Nicht verifiziert"] = strconv.Itoa(unverified)
	switch {
	case len(groups) == 0:
		ch.Message = "Keine Sicherungen"
	case len(failed) > 0:
		ch.State = model.StateCrit
		ch.Message = fmt.Sprintf("Verifizierung fehlgeschlagen bei %d %s: %s", len(failed), plural(len(failed), "Gruppe", "Gruppen"), strings.Join(failed[:min(5, len(failed))], ", "))
		if len(failed) > 5 {
			ch.Message += " …"
		}
		ch.Facts["Fehlgeschlagen"] = strings.Join(failed, ", ")
	case unverified == len(groups):
		ch.Message = "Keine der neuesten Sicherungen ist verifiziert – Verify-Job einrichten?"
	case unverified > 0:
		ch.Message = fmt.Sprintf("Neueste Sicherungen verifiziert, %d noch ausstehend", unverified)
	default:
		ch.Message = "Alle neuesten Sicherungen verifiziert"
	}
	return ch
}

// groupChecks are "pbs:group:<store>:<ns>/<type>/<id>": the newest backup of every group.
// Host backups (proxmox-backup-client) are judged by backup.age; the age of VM and container
// groups is shown as group.age only, since Proxmox VE already judges them per guest.
func groupChecks(store string, snaps []snapshot, now time.Time) []model.Check {
	var out []model.Check
	for _, g := range groupsOf(snaps) {
		s := g.newest
		inst := store + ":" + groupLabel(s)
		name := groupLabel(s)
		if c := strings.TrimSpace(strings.SplitN(s.Comment, "\n", 2)[0]); c != "" {
			name += " " + c
		}
		ch := model.Check{Key: "pbs:group:" + store + ":" + s.NS + "/" + s.Type + "/" + s.ID, Name: name, Group: "Sicherungsgruppen", State: model.StateOK,
			Facts: map[string]string{"Datastore": store, "Sicherungen": strconv.Itoa(g.count), "Letzte Sicherung": fmtTime(float64(s.Time)), "Ziel": store}}
		if s.NS != "" {
			ch.Facts["Namespace"] = s.NS
		}
		age := max(0, now.Sub(time.Unix(int64(s.Time), 0)).Hours())
		metric := "group.age"
		if s.Type == "host" {
			metric = "backup.age"
		}
		ch.Metrics = []model.Metric{{Name: metric, Instance: inst, Value: round2(age), Unit: "h"}}
		if s.Size > 0 {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "backup.size", Instance: inst, Value: float64(s.Size), Unit: "B"})
			ch.Facts["Größe"] = fmtBytes(float64(s.Size))
		}
		ch.Message = "Letzte Sicherung " + fmtTime(float64(s.Time)) + " (vor " + fmtAge(time.Duration(age*float64(time.Hour))) + ")"
		if s.Verification != nil {
			ch.Facts["Verifiziert"] = map[string]string{"ok": "ja", "failed": "fehlgeschlagen"}[s.Verification.State]
		}
		out = append(out, ch)
	}
	return out
}

var taskKinds = map[string]string{
	"verificationjob": "Verify-Job", "verify": "Verifizierung", "syncjob": "Sync-Job", "prunejob": "Prune-Job", "prune": "Prune",
	"garbage_collection": "Garbage Collection",
}

// taskChecks are "pbs:task:<type>:<id>": the outcome of the newest finished verify, sync,
// prune and garbage collection task of every job or datastore in the last 7 days (an error
// is critical, warnings are a warning).
func taskChecks(tasks []task, now time.Time) []model.Check {
	type last struct {
		t       task
		running bool
	}
	latest := map[string]*last{}
	var keys []string
	for _, t := range tasks {
		if _, ok := taskKinds[t.WorkerType]; !ok {
			continue
		}
		k := t.WorkerType + ":" + t.WorkerID
		l := latest[k]
		if l == nil {
			l = &last{}
			latest[k] = l
			keys = append(keys, k)
		}
		if t.End == 0 || t.Status == "" {
			l.running = true
			continue
		}
		if t.End > l.t.End {
			l.t = t
		}
	}
	slices.Sort(keys)
	var out []model.Check
	for _, k := range keys {
		l := latest[k]
		if l.t.UPID == "" { // only a running task so far
			continue
		}
		t := l.t
		ch := model.Check{Key: "pbs:task:" + k, Name: taskKinds[t.WorkerType] + " " + t.WorkerID, Group: "Jobs", State: model.StateOK,
			Facts: map[string]string{"Typ": taskKinds[t.WorkerType], "Letzter Lauf": fmtTime(float64(t.End))}}
		age := max(0, now.Sub(time.Unix(int64(t.End), 0)).Hours())
		ch.Metrics = []model.Metric{{Name: "job.age", Instance: k, Value: round2(age), Unit: "h"}}
		when := fmtTime(float64(t.End)) + " (vor " + fmtAge(time.Duration(age*float64(time.Hour))) + ")"
		switch {
		case t.Status == "OK":
			ch.Facts["Ergebnis"] = "Erfolgreich"
			ch.Message = "Erfolgreich · " + when
		case strings.HasPrefix(t.Status, "WARNINGS"):
			ch.State = model.StateWarn
			ch.Facts["Ergebnis"] = "Mit Warnungen"
			ch.Message = "Mit Warnungen · " + when
		default:
			ch.State = model.StateCrit
			ch.Facts["Ergebnis"] = "Fehlgeschlagen"
			ch.Message = "Fehlgeschlagen · " + when + " · " + t.Status
		}
		if l.running {
			ch.Message = "Läuft gerade · zuletzt " + ch.Message
		}
		out = append(out, ch)
	}
	return out
}

// TestResult is the answer of a connection test.
type TestResult struct {
	Version    string   `json:"version"`
	Datastores []string `json:"datastores"`
}

// Test checks the token and lists the datastores.
func Test(ctx context.Context, host string, cfg model.PBSConfig) (TestResult, error) {
	cl := newClient(host, cfg)
	defer cl.http.CloseIdleConnections()
	var ver version
	if err := cl.get(ctx, "/version", nil, &ver); err != nil {
		return TestResult{}, err
	}
	var usage []dsUsage
	if err := cl.get(ctx, "/status/datastore-usage", nil, &usage); err != nil {
		return TestResult{}, err
	}
	res := TestResult{Version: ver.Version, Datastores: []string{}}
	for _, u := range usage {
		res.Datastores = append(res.Datastores, u.Store)
	}
	if len(res.Datastores) == 0 {
		return res, errors.New("Verbunden, aber kein Datastore sichtbar – hat der Token die Rolle Audit (Datastore.Audit)?")
	}
	return res, nil
}
