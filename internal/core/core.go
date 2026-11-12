package core

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/collect/nut"
	"pk95monitoring/internal/collect/proxmox"
	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

// Store keys.
const (
	KindDevice     = "device"     // object kind of model.Device
	keySettings    = "settings"   // Settings
	keyEnrollKey   = "enroll_key" // agent enrollment key
	maxPolls       = 16           // concurrent polls
	maxPollTimeout = 45 * time.Second
	notifyTimeout  = 60 * time.Second
)

// timings are the periods of the background jobs (shortened in tests).
type timings struct {
	silence  time.Duration // agent silence detection, alert re-sync and re-notify
	rollup   time.Duration
	prune    time.Duration
	persist  time.Duration // debounce of status writes
	maxSleep time.Duration // longest scheduler sleep without a wake-up
	jitter   time.Duration // upper bound of the initial poll jitter
}

func defaultTimings() timings {
	return timings{silence: 30 * time.Second, rollup: time.Minute, prune: time.Hour,
		persist: 10 * time.Second, maxSleep: 30 * time.Second, jitter: 10 * time.Second}
}

// DefaultSettings are the settings on first start.
func DefaultSettings() Settings {
	return Settings{
		General: GeneralSettings{RetentionRawDays: store.DefaultRawDays, Retention5mDays: store.DefaultFiveDays,
			Retention1hDays: store.DefaultHourDays},
		Notify:     notify.Defaults(),
		Thresholds: []model.Threshold{},
		Central:    CentralSettings{Interval: 5},
		Agent:      AgentSettings{AutoUpdate: true},
	}
}

// New creates the engine (loads settings, devices and persisted statuses).
func New(st *store.Store, opt Options) (*Core, error) {
	c := &Core{
		St: st, Opt: opt, Started: time.Now().Unix(),
		Agents:  agentdist.NewCatalog(opt.AgentDir, opt.Version),
		Proxmox: proxmox.New(), SNMP: snmp.New(), NUT: nut.New(),
		Notify: notify.Send, Now: time.Now,
		devices: map[string]model.Device{}, rt: map[string]*devState{}, open: map[string]*openAlert{},
		wake: make(chan struct{}, 1), tm: defaultTimings(),
	}
	c.settings = DefaultSettings()
	if err := st.GetSetting(keySettings, &c.settings); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if c.settings.Thresholds == nil {
		c.settings.Thresholds = []model.Threshold{}
	}
	if err := st.GetSetting(keyEnrollKey, &c.enroll); err != nil {
		return nil, fmt.Errorf("enroll key: %w", err)
	}
	devs, err := store.List[model.Device](st, KindDevice)
	if err != nil {
		return nil, fmt.Errorf("devices: %w", err)
	}
	statuses, err := st.LoadStatuses()
	if err != nil {
		return nil, fmt.Errorf("statuses: %w", err)
	}
	for _, d := range devs {
		c.devices[d.ID] = d
		rt := newDevState(d.ID)
		if s, ok := statuses[d.ID]; ok {
			rt.st = s
			rt.reindex()
			if rt.st.Checks == nil {
				rt.st.Checks = []model.CheckStatus{}
			}
			for _, cs := range s.Checks {
				// The threshold part of the state is not persisted: when the effective state
				// (before a quiet mode) is worse than the native one, thresholds caused it.
				if v := verdict(cs); model.Rank(v) > model.Rank(cs.Native) {
					rt.thr[cs.Key] = v
				}
				if cs.Key == reachKey && cs.State != model.StateOK {
					rt.fails = 1
					if cs.State == model.StateCrit {
						rt.fails = 2
					}
				}
			}
		}
		if d.Agent != nil {
			rt.saved = d.Agent.LastSeen
		}
		c.rt[d.ID] = rt
	}
	alerts, err := st.ActiveAlerts()
	if err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	slices.Reverse(alerts) // oldest first, so a duplicate (newer) one is closed
	for _, a := range alerts {
		k := alertKey(a.DeviceID, a.CheckKey)
		if _, dup := c.open[k]; dup || c.devices[a.DeviceID].ID == "" {
			_ = st.CloseAlert(a.ID, 0)
			continue
		}
		c.open[k] = &openAlert{a: a, notified: a.Started, peak: a.Severity}
	}
	return c, nil
}

// Run starts the scheduler, agent-silence detection, rollup/retention and the central push.
// It blocks until ctx is done.
func (c *Core) Run(ctx context.Context) {
	c.start()
	c.running.Add(1)
	go func() {
		defer c.running.Done()
		c.schedule(ctx)
	}()

	silence := time.NewTicker(c.tm.silence)
	rollup := time.NewTicker(c.tm.rollup)
	prune := time.NewTicker(c.tm.prune)
	persist := time.NewTicker(c.tm.persist)
	defer silence.Stop()
	defer rollup.Stop()
	defer prune.Stop()
	defer persist.Stop()
	c.centralNext = c.Now().Add(time.Minute)
	pruneSoon := time.After(2 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			c.running.Wait()
			c.persist(true)
			c.waitNotify(5 * time.Second)
			return
		case <-silence.C:
			c.checkSilence()
			c.syncAllAlerts()
			c.renotify()
			if c.centralReset.Swap(false) {
				c.centralNext = time.Time{}
			}
			if now := c.Now(); !now.Before(c.centralNext) {
				c.centralNext = now.Add(time.Duration(max(c.Settings().Central.Interval, 1)) * time.Minute)
				c.running.Add(1)
				go func() {
					defer c.running.Done()
					c.pushCentral(ctx)
				}()
			}
		case <-rollup.C:
			if err := c.St.Rollup(c.Now().Unix()); err != nil {
				log.Printf("rollup: %v", err)
			}
		case <-pruneSoon:
			c.prune()
		case <-prune.C:
			c.prune()
		case <-persist.C:
			c.persist(false)
		}
	}
}

// start records the start time: agents get a full silence period to report again, and the
// re-notify period of alerts restored from the store counts from now (the time of their
// last notification is not persisted, so a restart would otherwise repeat them all at once).
func (c *Core) start() {
	now := c.Now().Unix()
	c.runStart.Store(now)
	c.mu.Lock()
	for _, oa := range c.open {
		oa.notified = max(oa.notified, now)
	}
	c.mu.Unlock()
}

// waitNotify waits (at most d) for notifications still being sent.
func (c *Core) waitNotify(d time.Duration) {
	done := make(chan struct{})
	go func() {
		c.notifyWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		log.Printf("notifications still pending at shutdown")
	}
}

// prune applies the retention settings.
func (c *Core) prune() {
	g := c.Settings().General
	now := c.Now()
	if err := c.St.PruneMetrics(now.Unix(), g.RetentionRawDays, g.Retention5mDays, g.Retention1hDays); err != nil {
		log.Printf("prune metrics: %v", err)
	}
	if err := c.St.PruneAlerts(now.Add(-400 * 24 * time.Hour).Unix()); err != nil {
		log.Printf("prune alerts: %v", err)
	}
	if err := c.St.PruneEvents(180 * 24 * time.Hour); err != nil {
		log.Printf("prune events: %v", err)
	}
	if err := c.St.PruneSessions(); err != nil {
		log.Printf("prune sessions: %v", err)
	}
}

// ---- settings ----

// Settings returns the current settings (with secrets).
func (c *Core) Settings() Settings {
	c.smu.RLock()
	defer c.smu.RUnlock()
	s := c.settings
	s.Thresholds = slices.Clone(s.Thresholds)
	if s.Thresholds == nil {
		s.Thresholds = []model.Threshold{}
	}
	s.Agent.WindowsServices = append([]string{}, s.Agent.WindowsServices...)
	s.Agent.LinuxServices = append([]string{}, s.Agent.LinuxServices...)
	return s
}

// SaveSettings validates and stores s. Empty secrets keep the stored values. Field errors are
// keyed like the JSON ("general.retention_raw_days", "central.url", "thresholds.0.metric").
// With field errors nothing is stored and the error is nil.
func (c *Core) SaveSettings(s Settings) (map[string]string, error) {
	errs := map[string]string{}
	g := &s.General
	g.Instance = strings.TrimSpace(g.Instance)
	g.PublicURL = strings.TrimRight(strings.TrimSpace(g.PublicURL), "/")
	if len([]rune(g.Instance)) > 100 {
		errs["general.instance"] = "Höchstens 100 Zeichen."
	}
	if g.PublicURL != "" && !validHTTPURL(g.PublicURL) {
		errs["general.public_url"] = "Adresse mit http:// oder https:// angeben."
	}
	days := func(key string, v *int, def, lo, hi int) {
		if *v == 0 {
			*v = def
		}
		if *v < lo || *v > hi {
			errs[key] = fmt.Sprintf("Wert zwischen %d und %d Tagen angeben.", lo, hi)
		}
	}
	days("general.retention_raw_days", &g.RetentionRawDays, store.DefaultRawDays, 1, 30)
	days("general.retention_5m_days", &g.Retention5mDays, store.DefaultFiveDays, 1, 400)
	days("general.retention_1h_days", &g.Retention1hDays, store.DefaultHourDays, 1, 3650)
	if g.RenotifyHours < 0 || g.RenotifyHours > 720 {
		errs["general.renotify_hours"] = "0 (nie) oder 1 bis 720 Stunden."
	}

	n := &s.Notify
	if n.MinSeverity == "" {
		n.MinSeverity = "warning"
	}
	if !slices.Contains([]string{"info", "warning", "critical"}, n.MinSeverity) {
		errs["notify.min_severity"] = "Ungültige Stufe."
	}
	n.SMTPHost, n.MailFrom, n.MailTo = strings.TrimSpace(n.SMTPHost), strings.TrimSpace(n.MailFrom), strings.TrimSpace(n.MailTo)
	n.HookURL, n.TelegramChat = strings.TrimSpace(n.HookURL), strings.TrimSpace(n.TelegramChat)
	if n.SMTPPort == 0 {
		n.SMTPPort = 587
	}
	if n.SMTPPort < 1 || n.SMTPPort > 65535 {
		errs["notify.smtp_port"] = "Port zwischen 1 und 65535."
	}
	if n.SMTPSec == "" {
		n.SMTPSec = "starttls"
	}
	if !slices.Contains([]string{"starttls", "tls", "none"}, n.SMTPSec) {
		errs["notify.smtp_security"] = "Ungültige Verschlüsselung."
	}
	if n.MailEnabled {
		if n.SMTPHost == "" {
			errs["notify.smtp_host"] = "SMTP-Server angeben."
		}
		if n.MailFrom == "" {
			errs["notify.mail_from"] = "Absender angeben."
		}
		if n.MailTo == "" {
			errs["notify.mail_to"] = "Mindestens einen Empfänger angeben."
		}
	}
	if n.HookKind == "" {
		n.HookKind = "ntfy"
	}
	if !slices.Contains([]string{"ntfy", "gotify", "json"}, n.HookKind) {
		errs["notify.hook_kind"] = "Ungültige Art."
	}
	if n.HookEnabled && !validHTTPURL(n.HookURL) {
		errs["notify.hook_url"] = "Adresse mit http:// oder https:// angeben."
	}
	old := c.Settings()
	if n.SMTPPass == "" {
		n.SMTPPass = old.Notify.SMTPPass
	}
	if n.HookToken == "" {
		n.HookToken = old.Notify.HookToken
	}
	if n.TelegramToken == "" {
		n.TelegramToken = old.Notify.TelegramToken
	}
	if n.TelegramEnabled {
		if !notify.ValidTelegramToken(n.TelegramToken) {
			errs["notify.telegram_token"] = "Bot-Token im Format 123456:ABC… angeben."
		}
		if !notify.ValidTelegramChat(n.TelegramChat) {
			errs["notify.telegram_chat"] = "Chat-ID (Zahl) oder @Kanal angeben."
		}
	}

	if s.Thresholds == nil {
		s.Thresholds = []model.Threshold{}
	}
	validateThresholds(s.Thresholds, errs)

	ce := &s.Central
	ce.URL = strings.TrimSpace(ce.URL)
	if ce.Interval == 0 {
		ce.Interval = 5
	}
	if ce.Interval < 1 || ce.Interval > 1440 {
		errs["central.interval"] = "Intervall zwischen 1 und 1440 Minuten."
	}
	if ce.Token == "" {
		ce.Token = old.Central.Token
	}
	if ce.Enabled || ce.URL != "" {
		if !validHTTPURL(ce.URL) {
			errs["central.url"] = "Adresse mit http:// oder https:// angeben."
		}
	}
	if ce.Enabled && ce.Token == "" {
		errs["central.token"] = "Token angeben."
	}

	ag := &s.Agent
	ag.WindowsServices, ag.LinuxServices = cleanList(ag.WindowsServices), cleanList(ag.LinuxServices)
	for key, list := range map[string][]string{"agent.windows_services": ag.WindowsServices, "agent.linux_services": ag.LinuxServices} {
		if len(list) > maxServiceEntries {
			errs[key] = fmt.Sprintf("Höchstens %d Einträge.", maxServiceEntries)
		}
		for i, e := range list {
			if len(e) > 256 || strings.ContainsAny(e, "\n\r\t") {
				errs[key] = fmt.Sprintf("Eintrag %d ist ungültig.", i+1)
			}
		}
	}
	if len(errs) > 0 {
		return errs, nil
	}
	if err := c.St.SetSetting(keySettings, s); err != nil {
		return nil, err
	}
	c.smu.Lock()
	c.settings = s
	c.smu.Unlock()
	if s.Central != old.Central {
		c.centralReset.Store(true) // push soon with the new settings, not after the old interval
	}
	if !slices.Equal(s.Agent.WindowsServices, old.Agent.WindowsServices) || !slices.Equal(s.Agent.LinuxServices, old.Agent.LinuxServices) {
		// The agents report the new selection with their next report: those services are
		// expected, not discovered.
		c.mu.Lock()
		for id, d := range c.devices {
			if d.Type == model.TypeAgent && c.rt[id] != nil {
				c.rt[id].acceptUnseen = true
			}
		}
		c.mu.Unlock()
	}
	return nil, nil
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// InstanceName returns the instance name (environment wins over the setting).
func (c *Core) InstanceName() string {
	if c.Opt.Instance != "" {
		return c.Opt.Instance
	}
	return c.Settings().General.Instance
}

// PublicURL returns the configured public URL (environment wins), "" if none.
func (c *Core) PublicURL() string {
	if c.Opt.PublicURL != "" {
		return strings.TrimRight(c.Opt.PublicURL, "/")
	}
	return c.Settings().General.PublicURL
}

// enrollAlphabet avoids look-alike characters (0/O, 1/I/L).
const enrollAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func randomString(alphabet string, n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// newID returns a random lower-case id of n characters.
func newID(n int) string { return randomString("abcdefghijklmnopqrstuvwxyz0123456789", n) }

// EnrollKey returns the agent enrollment key (created on first use).
func (c *Core) EnrollKey() string {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.enroll == "" {
		k := randomString(enrollAlphabet, 24)
		if err := c.St.SetSetting(keyEnrollKey, k); err != nil {
			log.Printf("enroll key: %v", err)
			return ""
		}
		c.enroll = k
	}
	return c.enroll
}

// RotateEnrollKey replaces the enrollment key.
func (c *Core) RotateEnrollKey() (string, error) {
	k := randomString(enrollAlphabet, 24)
	c.smu.Lock()
	defer c.smu.Unlock()
	if err := c.St.SetSetting(keyEnrollKey, k); err != nil {
		return "", err
	}
	c.enroll = k
	return k, nil
}

// checkEnrollKey compares in constant time (case and surrounding blanks are ignored).
func (c *Core) checkEnrollKey(k string) bool {
	want := c.EnrollKey()
	got := strings.ToUpper(strings.TrimSpace(k))
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// TestNotify sends a test message with the given (unsaved) notify settings; empty secrets are
// taken from the stored settings.
func (c *Core) TestNotify(ctx context.Context, s notify.Settings) error {
	old := c.Settings().Notify
	if s.SMTPPass == "" {
		s.SMTPPass = old.SMTPPass
	}
	if s.HookToken == "" {
		s.HookToken = old.HookToken
	}
	if s.TelegramToken == "" {
		s.TelegramToken = old.TelegramToken
	}
	if !channelsOn(s) {
		return errors.New("Kein Benachrichtigungskanal aktiviert")
	}
	m := c.message("info", "PK95 Monitoring", "Testnachricht",
		"Diese Testnachricht bestätigt, dass die Benachrichtigungen ankommen.", nil)
	return c.Notify(ctx, s, m)
}

// ---- events and notifications ----

func channelsOn(s notify.Settings) bool { return s.MailEnabled || s.HookEnabled || s.TelegramEnabled }

// Event writes an event to the log (and notifies when severity passes the notify filter
// and notifyIt is true).
func (c *Core) Event(severity, source, title, body string, details *notify.Details, notifyIt bool) {
	c.event(severity, source, title, body, details, notifyIt, severity)
}

// event writes an event; the notification is filtered by gate instead of severity (a
// "Wieder OK" info goes out when the alert itself was wanted).
func (c *Core) event(severity, source, title, body string, details *notify.Details, notifyIt bool, gate string) {
	ev := store.Event{TS: c.Now().Unix(), Severity: severity, Source: source, Title: title, Body: body}
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			ev.Details = b
		}
	}
	if _, err := c.St.AddEvent(ev); err != nil {
		log.Printf("event: %v", err)
	}
	log.Printf("[%s] %s: %s %s", severity, source, title, strings.ReplaceAll(body, "\n", " "))
	if notifyIt {
		c.send(c.message(severity, source, title, body, details), gate)
	}
}

// message builds a notification with the instance name and address.
func (c *Core) message(severity, source, title, body string, d *notify.Details) notify.Message {
	host := c.InstanceName()
	if host == "" {
		host, _ = os.Hostname()
	}
	return notify.Message{Severity: severity, Source: source, Title: title, Body: body, Details: d,
		Host: host, URL: c.PublicURL(), Version: c.Opt.Version, Time: c.Now()}
}

// send delivers m in the background when a channel is on and gate passes the filter.
// The first failure becomes a warning event; further ones are only logged until a
// notification succeeds again.
func (c *Core) send(m notify.Message, gate string) {
	ns := c.Settings().Notify
	if !channelsOn(ns) || !ns.Wants(gate) {
		return
	}
	c.notifyWG.Add(1)
	go func() {
		defer c.notifyWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		err := c.Notify(ctx, ns, m)
		if err == nil {
			c.notifyFail.Store(false)
			return
		}
		log.Printf("notify: %v", err)
		if c.notifyFail.CompareAndSwap(false, true) {
			ev := store.Event{TS: c.Now().Unix(), Severity: "warning", Source: "Benachrichtigung",
				Title: "Benachrichtigung fehlgeschlagen", Body: err.Error()}
			if _, err := c.St.AddEvent(ev); err != nil {
				log.Printf("event: %v", err)
			}
		}
	}()
}

// persist saves changed statuses (all = also unchanged agent devices with a newer LastSeen).
func (c *Core) persist(final bool) {
	c.pmu.Lock() // a device deleted meanwhile must not be written back
	defer c.pmu.Unlock()
	c.mu.Lock()
	var sts []model.DeviceStatus
	var devs []model.Device
	for id, rt := range c.rt {
		if rt.dirty {
			rt.dirty = false
			sts = append(sts, cloneStatus(rt.st))
		}
		if d := c.devices[id]; final && d.Agent != nil && d.Agent.LastSeen > rt.saved {
			rt.saved = d.Agent.LastSeen
			devs = append(devs, d)
		}
	}
	c.mu.Unlock()
	for _, s := range sts {
		if err := c.St.SaveStatus(s); err != nil {
			log.Printf("save status %s: %v", s.DeviceID, err)
		}
	}
	for _, d := range devs {
		if err := c.St.Put(KindDevice, d.ID, d); err != nil {
			log.Printf("save device %s: %v", d.ID, err)
		}
	}
}

// fmtTime formats a unix time for messages ("14:05" today, else "03.11. 14:05").
func (c *Core) fmtTime(ts int64) string {
	if ts == 0 {
		return "nie"
	}
	t, now := time.Unix(ts, 0).Local(), c.Now().Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("02.01. 15:04")
	}
	return t.Format("02.01.2006 15:04")
}
