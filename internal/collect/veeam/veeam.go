// Package veeam polls Veeam Backup & Replication 12 or newer over its REST API (HTTPS, port
// 9419): the server version, the state of every job (last result, last and next run) and the
// fill level of the backup repositories.
package veeam

import (
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
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// DefaultPort is the port of the Veeam REST API.
const DefaultPort = 9419

// apiVersion is sent with every request; Veeam 12.0 and newer accept it.
const apiVersion = "1.1-rev0"

const requestTimeout = 30 * time.Second

// Collector polls Veeam servers. It keeps a token per device between polls.
type Collector struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*client
}

// New returns a Collector.
func New() *Collector { return &Collector{now: time.Now, clients: map[string]*client{}} }

// Forget drops the connection of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl := c.clients[deviceID]; cl != nil {
		cl.http.CloseIdleConnections()
	}
	delete(c.clients, deviceID)
}

type client struct {
	base    string
	user    string
	pass    string
	http    *http.Client
	sig     string
	mu      sync.Mutex
	token   string
	expires time.Time
}

func port(p int) int {
	if p <= 0 {
		return DefaultPort
	}
	return p
}

// normFingerprint is a SHA-256 fingerprint as lower-case hex without separators.
func normFingerprint(f string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(f)))
}

func newClient(host string, cfg model.VeeamConfig) *client {
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
	p := port(cfg.Port)
	return &client{base: "https://" + net.JoinHostPort(host, strconv.Itoa(p)), user: strings.TrimSpace(cfg.Username), pass: cfg.Password,
		http: &http.Client{Transport: tr, Timeout: requestTimeout},
		sig:  strings.Join([]string{host, strconv.Itoa(p), cfg.Username, cfg.Password, normFingerprint(cfg.Fingerprint)}, "\x00")}
}

func (c *Collector) clientFor(d model.Device) *client {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := newClient(d.Host, *d.Veeam)
	if old := c.clients[d.ID]; old != nil {
		if old.sig == cl.sig {
			return old
		}
		old.http.CloseIdleConnections()
	}
	c.clients[d.ID] = cl
	return cl
}

// friendly turns transport errors into German messages.
func friendly(err error) error {
	var cert *tls.CertificateVerificationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &cert) || strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "Fingerabdruck"):
		return errors.New("Zertifikat nicht vertrauenswürdig – Fingerabdruck des Veeam-Servers übernehmen (" + err.Error() + ")")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("Verbindung abgelehnt – läuft der Veeam-REST-Dienst (Port 9419)?")
	case strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "i/o timeout"):
		return errors.New("Zeitüberschreitung – Host und Port 9419 erreichbar (Firewall)?")
	}
	return err
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "Anmeldung abgelehnt – Benutzer und Kennwort prüfen"
	case http.StatusForbidden:
		return "Keine Berechtigung – der Benutzer braucht in Veeam mindestens die Rolle „Restore Operator“"
	}
	if e.msg != "" {
		return fmt.Sprintf("Veeam antwortet mit HTTP %d: %s", e.status, e.msg)
	}
	return fmt.Sprintf("Veeam antwortet mit HTTP %d", e.status)
}

func readErr(resp *http.Response) error {
	var body struct {
		Message string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(b, &body)
	return &apiError{status: resp.StatusCode, msg: body.Message}
}

// login gets a token (password grant).
func (cl *client) login(ctx context.Context) error {
	form := url.Values{"grant_type": {"password"}, "username": {cl.user}, "password": {cl.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.base+"/api/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-api-version", apiVersion)
	resp, err := cl.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return &apiError{status: http.StatusUnauthorized}
	}
	if resp.StatusCode != http.StatusOK {
		return readErr(resp)
	}
	var tok struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.Token == "" {
		return errors.New("Antwort der Anmeldung nicht lesbar – ist das wirklich die Veeam-REST-API?")
	}
	cl.token, cl.expires = tok.Token, time.Now().Add(time.Duration(max(60, tok.Expires-60))*time.Second)
	return nil
}

// get calls GET path (with a fresh token when needed) and decodes the answer.
func (cl *client) get(ctx context.Context, path string, out any) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	for attempt := 0; ; attempt++ {
		if cl.token == "" || time.Now().After(cl.expires) {
			if err := cl.login(ctx); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("x-api-version", apiVersion)
		req.Header.Set("Authorization", "Bearer "+cl.token)
		req.Header.Set("Accept", "application/json")
		resp, err := cl.http.Do(req)
		if err != nil {
			return friendly(err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			cl.token = "" // expired early: log in again once
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return readErr(resp)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
			return fmt.Errorf("Antwort von %s nicht lesbar: %w", path, err)
		}
		return nil
	}
}

type serverInfo struct {
	Name         string `json:"name"`
	BuildVersion string `json:"buildVersion"`
	Database     string `json:"databaseVendor"`
}

type jobState struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"` // running, inactive, disabled
	LastRun    string `json:"lastRun"`
	NextRun    string `json:"nextRun"`
	LastResult string `json:"lastResult"` // None, Success, Warning, Failed
	Workload   string `json:"workload"`
	Objects    int    `json:"objectsCount"`
	Repository string `json:"repositoryName"`
}

type repoState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	HostName string  `json:"hostName"`
	Path     string  `json:"path"`
	Capacity float64 `json:"capacityGB"`
	Free     float64 `json:"freeGB"`
	Used     float64 `json:"usedSpaceGB"`
}

type page[T any] struct {
	Data []T `json:"data"`
}

// Collect polls one Veeam server. It returns an error when the server cannot be reached or
// the login fails; a failing repository list only leaves out the repositories.
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.Veeam == nil || strings.TrimSpace(d.Veeam.Username) == "" {
		return nil, errors.New("Veeam-Zugang fehlt (Benutzer und Kennwort)")
	}
	if strings.TrimSpace(d.Host) == "" {
		return nil, errors.New("Host fehlt")
	}
	cl := c.clientFor(d)
	var info serverInfo
	if err := cl.get(ctx, "/api/v1/serverInfo", &info); err != nil {
		return nil, err
	}
	var jobs page[jobState]
	if err := cl.get(ctx, "/api/v1/jobs/states?limit=1000", &jobs); err != nil {
		return nil, err
	}
	now := c.now()
	checks := []model.Check{serverCheck(info, len(jobs.Data))}
	for _, j := range jobs.Data {
		checks = append(checks, jobCheck(j, now))
	}
	var repos page[repoState]
	if err := cl.get(ctx, "/api/v1/backupInfrastructure/repositories/states?limit=1000", &repos); err == nil {
		for _, r := range repos.Data {
			checks = append(checks, repoCheck(r))
		}
	}
	return checks, nil
}

// TestResult is the answer of a connection test.
type TestResult struct {
	Version string   `json:"version"`
	Name    string   `json:"name"`
	Jobs    []string `json:"jobs"` // job names
}

// Test logs in and lists the jobs.
func Test(ctx context.Context, host string, cfg model.VeeamConfig) (TestResult, error) {
	cl := newClient(host, cfg)
	defer cl.http.CloseIdleConnections()
	var info serverInfo
	if err := cl.get(ctx, "/api/v1/serverInfo", &info); err != nil {
		return TestResult{}, err
	}
	var jobs page[jobState]
	if err := cl.get(ctx, "/api/v1/jobs/states?limit=1000", &jobs); err != nil {
		return TestResult{}, err
	}
	res := TestResult{Version: info.BuildVersion, Name: info.Name, Jobs: []string{}}
	for _, j := range jobs.Data {
		res.Jobs = append(res.Jobs, j.Name)
	}
	return res, nil
}

func serverCheck(info serverInfo, jobs int) model.Check {
	ch := model.Check{Key: "veeam:server", Name: "Veeam-Server", Group: "Server", State: model.StateOK,
		Message: "Veeam Backup & Replication " + info.BuildVersion + " · " + strconv.Itoa(jobs) + " Jobs",
		Facts:   map[string]string{"Version": info.BuildVersion}}
	if info.Name != "" {
		ch.Facts["Name"] = info.Name
	}
	if info.Database != "" {
		ch.Facts["Datenbank"] = info.Database
	}
	return ch
}

var jobTypes = map[string]string{
	"Backup": "VM-Sicherung", "BackupCopy": "Sicherungskopie", "Replica": "Replikation", "EntraIDTenantBackup": "Entra ID",
	"WindowsAgentBackup": "Agent (Windows)", "LinuxAgentBackup": "Agent (Linux)", "WindowsAgentBackupWorkstationPolicy": "Agent-Richtlinie (Windows)",
	"LinuxAgentBackupWorkstationPolicy": "Agent-Richtlinie (Linux)", "FileBackup": "Dateifreigaben", "ObjectStorageBackup": "Objektspeicher",
	"SureBackup": "SureBackup", "CloudDirectorBackup": "Cloud Director",
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t
}

func fmtTime(t time.Time) string { return t.Local().Format("02.01.2006 15:04") }

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

// overdueAfter: a scheduled run this long in the past that did not start is reported.
const overdueAfter = 2 * time.Hour

// jobCheck turns a job state into the check "veeam:job:<id>": the last result decides (Failed
// crit, Warning warn), a scheduled run that did not start is a warning. The metric job.age is
// the age of the last run in hours.
func jobCheck(j jobState, now time.Time) model.Check {
	ch := model.Check{Key: "veeam:job:" + j.ID, Name: j.Name, Group: "Jobs", State: model.StateOK, Facts: map[string]string{}}
	typ := jobTypes[j.Type]
	if typ == "" {
		typ = j.Type
	}
	ch.Facts["Typ"] = typ
	if j.Repository != "" {
		ch.Facts["Ziel"] = j.Repository
	}
	if j.Objects > 0 {
		ch.Facts["Objekte"] = strconv.Itoa(j.Objects)
	}
	last, next := parseTime(j.LastRun), parseTime(j.NextRun)
	if !last.IsZero() {
		ch.Facts["Letzter Lauf"] = fmtTime(last)
		ch.Metrics = []model.Metric{{Name: "job.age", Instance: j.Name, Value: max(0, float64(int(now.Sub(last).Minutes()))/60), Unit: "h"}}
	}
	if !next.IsZero() {
		ch.Facts["Nächster Lauf"] = fmtTime(next)
	}
	result := map[string]string{"Success": "Erfolgreich", "Warning": "Mit Warnungen", "Failed": "Fehlgeschlagen"}[j.LastResult]
	if result != "" {
		ch.Facts["Ergebnis"] = result
	}
	if strings.EqualFold(j.Status, "disabled") {
		ch.Message = "Deaktiviert"
		if result != "" {
			ch.Message += " · zuletzt " + strings.ToLower(result)
		}
		ch.Metrics = nil
		return ch
	}
	var parts []string
	switch {
	case strings.EqualFold(j.Status, "running"):
		parts = append(parts, "Läuft gerade")
		if result != "" {
			parts = append(parts, "zuletzt "+strings.ToLower(result))
		}
	case result != "":
		parts = append(parts, result)
	default:
		parts = append(parts, "Noch nicht gelaufen")
	}
	if !last.IsZero() {
		parts = append(parts, fmtTime(last)+" (vor "+fmtAge(now.Sub(last))+")")
	}
	if !next.IsZero() && next.After(now) {
		parts = append(parts, "nächster Lauf "+fmtTime(next))
	}
	ch.Message = strings.Join(parts, " · ")
	switch j.LastResult {
	case "Failed":
		ch.State = model.StateCrit
	case "Warning":
		ch.State = model.StateWarn
	}
	if !next.IsZero() && now.Sub(next) > overdueAfter && !strings.EqualFold(j.Status, "running") {
		ch.State = model.Worst(ch.State, model.StateWarn)
		ch.Message = "Geplanter Lauf am " + fmtTime(next) + " ist nicht gestartet · " + ch.Message
	}
	return ch
}

// repoCheck turns a repository into "veeam:repo:<id>" with its fill level (storage.usage);
// repositories without a known capacity (object storage) have no metrics.
func repoCheck(r repoState) model.Check {
	ch := model.Check{Key: "veeam:repo:" + r.ID, Name: "Repository " + r.Name, Group: "Repositories", State: model.StateOK, Facts: map[string]string{}}
	if r.Path != "" {
		ch.Facts["Pfad"] = r.Path
	}
	if r.HostName != "" {
		ch.Facts["Host"] = r.HostName
	}
	if r.Type != "" {
		ch.Facts["Typ"] = r.Type
	}
	if r.Capacity <= 0 {
		ch.Message = "Kapazität unbekannt"
		return ch
	}
	used := r.Used
	if used <= 0 {
		used = r.Capacity - r.Free
	}
	gb := float64(1 << 30)
	usage := 100 * used / r.Capacity
	ch.Metrics = []model.Metric{{Name: "storage.usage", Instance: r.Name, Value: float64(int(usage*100)) / 100, Unit: "%"},
		{Name: "storage.used", Instance: r.Name, Value: used * gb, Unit: "B"}, {Name: "storage.total", Instance: r.Name, Value: r.Capacity * gb, Unit: "B"}}
	ch.Message = fmt.Sprintf("%.0f %% belegt · %s frei von %s", usage, fmtGB(r.Free), fmtGB(r.Capacity))
	return ch
}

func fmtGB(v float64) string {
	if v >= 1024 {
		return strings.Replace(fmt.Sprintf("%.1f TB", v/1024), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f GB", v)
}
