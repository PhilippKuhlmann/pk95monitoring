package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server updates (Einstellungen → Aktualisierung): the server looks for new releases on GitHub
// and shows them. A container cannot replace itself, so "Installieren" writes
// <data>/update/request.json; the host helper (deploy/updater.sh, a systemd path unit on the
// Docker host) runs the update there and reports in status.json and update.log. Without the
// helper the page shows the command to run by hand.

// Release source (tests point it to a local server).
var (
	UpdateRepo = "PhilippKuhlmann/pk95monitoring"
	GitHubAPI  = "https://api.github.com"
)

const (
	keyUpdate        = "update_state"
	updateCheckEvery = 12 * time.Hour
	updateLogTail    = 4000 // bytes of update.log shown after a failure
)

// UpdateInfo is what the settings page shows about updates.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Tag       string `json:"tag"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	URL       string `json:"url"`
	Published int64  `json:"published"`
	Checked   int64  `json:"checked"`
	Error     string `json:"error,omitempty"`
	AutoCheck bool   `json:"auto_check"`
	// Helper: the host helper is installed (mode update.sh or compose).
	Helper     bool   `json:"helper"`
	HelperMode string `json:"helper_mode,omitempty"`
	// Phase of the last update: requested (waiting for the helper), running, done, failed.
	Phase    string `json:"phase,omitempty"`
	PhaseTag string `json:"phase_tag,omitempty"`
	Started  int64  `json:"started,omitempty"`
	Finished int64  `json:"finished,omitempty"`
	Log      string `json:"log,omitempty"` // end of update.log after a failure
	// CanInstall: "Installieren" works; Reason says why not.
	CanInstall bool   `json:"can_install"`
	Reason     string `json:"reason,omitempty"`
	Repo       string `json:"repo"`
}

type updateState struct {
	Latest    string `json:"latest"`
	Tag       string `json:"tag"`
	Notes     string `json:"notes"`
	URL       string `json:"url"`
	Published int64  `json:"published"`
	Checked   int64  `json:"checked"`
	Error     string `json:"error,omitempty"`
	Notified  string `json:"notified,omitempty"` // newest version an event was written for
	AutoOff   bool   `json:"auto_off,omitempty"` // no regular look for new versions
	Reported  string `json:"reported,omitempty"` // "<tag>/<finished>" of the last reported helper result
}

// helperStatus is status.json of the host helper.
type helperStatus struct {
	State    string `json:"state"` // running, done, failed
	Tag      string `json:"tag"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished"`
	Code     int    `json:"code"`
}

type updateRuntime struct {
	mu       sync.Mutex
	checking bool
}

func (c *Core) updateDir() string { return filepath.Join(c.Opt.DataDir, "update") }

func (c *Core) updateState() updateState {
	var st updateState
	if err := c.St.GetSetting(keyUpdate, &st); err != nil {
		log.Printf("update state: %v", err)
	}
	return st
}

func (c *Core) saveUpdateState(st updateState) {
	if err := c.St.SetSetting(keyUpdate, st); err != nil {
		log.Printf("save update state: %v", err)
	}
}

// NewerVersion reports whether version a ("0.19.0") is newer than b. Unparsable versions (a
// development build) are never older.
func NewerVersion(a, b string) bool {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// readHelper returns the helper's installation mode ("" = not installed) and its last status.
func (c *Core) readHelper() (string, helperStatus, bool) {
	dir := c.updateDir()
	var h struct {
		Mode string `json:"mode"`
	}
	mode := ""
	if b, err := os.ReadFile(filepath.Join(dir, "helper.json")); err == nil && json.Unmarshal(b, &h) == nil {
		mode = h.Mode
		if mode == "" {
			mode = "compose"
		}
	}
	var st helperStatus
	b, err := os.ReadFile(filepath.Join(dir, "status.json"))
	ok := err == nil && json.Unmarshal(b, &st) == nil && st.State != ""
	return mode, st, ok
}

func (c *Core) requested() (string, bool) {
	b, err := os.ReadFile(filepath.Join(c.updateDir(), "request.json"))
	if err != nil {
		return "", false
	}
	var r struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(b, &r)
	return r.Tag, true
}

func logTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > updateLogTail {
		b = b[len(b)-updateLogTail:]
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return strings.ToValidUTF8(strings.TrimSpace(string(b)), "?")
}

// Update returns the known update state.
func (c *Core) Update() UpdateInfo {
	st := c.updateState()
	info := UpdateInfo{Current: c.Opt.Version, Latest: st.Latest, Tag: st.Tag, Notes: st.Notes, URL: st.URL, Published: st.Published,
		Checked: st.Checked, Error: st.Error, AutoCheck: !st.AutoOff, Repo: UpdateRepo}
	info.Available = st.Latest != "" && NewerVersion(st.Latest, c.Opt.Version)
	mode, hs, ok := c.readHelper()
	info.Helper, info.HelperMode = mode != "", mode
	if ok {
		info.Phase, info.PhaseTag, info.Started, info.Finished = hs.State, hs.Tag, hs.Started, hs.Finished
		if hs.State == "failed" {
			info.Log = logTail(filepath.Join(c.updateDir(), "update.log"))
		}
	}
	if tag, ok := c.requested(); ok {
		info.Phase, info.PhaseTag = "requested", tag
	}
	switch {
	case c.Opt.Demo:
		info.Reason = "In der Demo nicht möglich."
	case !info.Helper:
		info.Reason = "Der Update-Helfer ist auf dem Docker-Host nicht eingerichtet."
	case !info.Available:
		info.Reason = "Es ist keine neuere Version bekannt."
	case info.Phase == "requested" || info.Phase == "running":
		info.Reason = "Das Update läuft bereits."
	default:
		info.CanInstall = true
	}
	return info
}

// SetUpdateAutoCheck turns the regular look for new versions on or off.
func (c *Core) SetUpdateAutoCheck(on bool) error {
	st := c.updateState()
	st.AutoOff = !on
	return c.St.SetSetting(keyUpdate, st)
}

// CheckUpdate asks GitHub for the newest release.
func (c *Core) CheckUpdate(ctx context.Context) (UpdateInfo, error) {
	c.upd.mu.Lock()
	if c.upd.checking {
		c.upd.mu.Unlock()
		return c.Update(), nil
	}
	c.upd.checking = true
	c.upd.mu.Unlock()
	defer func() {
		c.upd.mu.Lock()
		c.upd.checking = false
		c.upd.mu.Unlock()
	}()
	st := c.updateState()
	st.Checked = c.Now().Unix()
	err := c.fetchRelease(ctx, &st)
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	}
	if err == nil && NewerVersion(st.Latest, c.Opt.Version) && st.Notified != st.Latest {
		st.Notified = st.Latest
		c.Event("info", "Server", "Version "+st.Latest+" verfügbar",
			"Installiert ist "+c.Opt.Version+". Die neue Version steht unter Einstellungen → Aktualisierung.", nil, false)
	}
	c.saveUpdateState(st)
	return c.Update(), err
}

func (c *Core) fetchRelease(ctx context.Context, st *updateState) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GitHubAPI+"/repos/"+UpdateRepo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	// No data about this installation goes out, only the program version.
	req.Header.Set("User-Agent", "pk95monitoring/"+c.Opt.Version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("GitHub nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("noch kein Release veröffentlicht")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub antwortet mit HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag       string    `json:"tag_name"`
		Body      string    `json:"body"`
		URL       string    `json:"html_url"`
		Published time.Time `json:"published_at"`
		Draft     bool      `json:"draft"`
		Pre       bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return errors.New("Antwort von GitHub nicht lesbar")
	}
	if _, ok := parseVersion(rel.Tag); !ok || rel.Draft || rel.Pre {
		return fmt.Errorf("ungewöhnliches Release %q", rel.Tag)
	}
	notes := []rune(strings.TrimSpace(rel.Body))
	if len(notes) > 4000 {
		notes = append(notes[:3999], '…')
	}
	st.Tag, st.Latest, st.Notes, st.URL, st.Published = rel.Tag, strings.TrimPrefix(rel.Tag, "v"), string(notes), rel.URL, rel.Published.Unix()
	return nil
}

// InstallUpdate asks the host helper to install the newest known release.
func (c *Core) InstallUpdate() error {
	info := c.Update()
	if !info.CanInstall {
		return errors.New(info.Reason)
	}
	b, _ := json.Marshal(map[string]any{"tag": info.Tag, "requested": c.Now().Unix()})
	tmp := filepath.Join(c.updateDir(), ".request.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("Anforderung nicht schreibbar: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(c.updateDir(), "request.json")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("Anforderung nicht schreibbar: %w", err)
	}
	c.Event("info", "Server", "Update auf "+info.Latest+" angefordert",
		"Der Update-Helfer auf dem Docker-Host installiert die neue Version; der Server startet dabei neu.", nil, false)
	return nil
}

// updateTick looks for new versions (every 12 h when on) and reports the result of the host
// helper once: after the restart into the new version, or when the update failed.
func (c *Core) updateTick(ctx context.Context) {
	if c.Opt.Demo || c.Opt.DataDir == "" { // the demo and tests without a data directory stay offline
		return
	}
	st := c.updateState()
	if !st.AutoOff && c.Now().Unix()-st.Checked >= int64(updateCheckEvery.Seconds()) {
		c.running.Add(1)
		go func() {
			defer c.running.Done()
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if _, err := c.CheckUpdate(cctx); err != nil {
				log.Printf("update check: %v", err)
			}
		}()
	}
	_, hs, ok := c.readHelper()
	if !ok || hs.State == "running" {
		return
	}
	key := hs.Tag + "/" + strconv.FormatInt(hs.Finished, 10)
	if st.Reported == key {
		return
	}
	version := strings.TrimPrefix(hs.Tag, "v")
	switch {
	case hs.State == "done" && version == c.Opt.Version:
		c.Event("info", "Server", "Auf Version "+version+" aktualisiert", "Der Update-Helfer hat die neue Version installiert.", nil, false)
	case hs.State == "done":
		c.Event("warning", "Server", "Update auf "+version+" ohne Wirkung",
			"Der Update-Helfer meldet Erfolg, es läuft aber weiterhin "+c.Opt.Version+". Steht in der compose-Datei ein festes Image-Tag statt :latest?", nil, false)
	default:
		c.Event("warning", "Server", "Update auf "+version+" fehlgeschlagen",
			"Es läuft weiterhin "+c.Opt.Version+". Das Protokoll steht unter Einstellungen → Aktualisierung.", nil, false)
	}
	st = c.updateState()
	st.Reported = key
	c.saveUpdateState(st)
}
