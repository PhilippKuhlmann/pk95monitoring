// Package model holds the types shared by the server, the collectors and the agent:
// devices, check results, thresholds, alerts and the agent protocol. It has no
// dependencies outside the standard library, so the agent binary stays small.
package model

import (
	"slices"
	"strings"
)

// Check and device states, from good to bad.
const (
	StateOK      = "ok"
	StateUnknown = "unknown" // no data yet, or the value could not be read
	StateWarn    = "warn"
	StateCrit    = "crit"
)

// Rank orders states by severity (higher is worse).
func Rank(state string) int {
	switch state {
	case StateOK:
		return 0
	case StateUnknown:
		return 1
	case StateWarn:
		return 2
	case StateCrit:
		return 3
	}
	return 1
}

// Worst returns the more severe of two states.
func Worst(a, b string) string {
	if Rank(b) > Rank(a) {
		return b
	}
	return a
}

// Device types.
const (
	TypeProxmox = "proxmox" // Proxmox VE node or cluster, polled over its REST API
	TypeAgent   = "agent"   // Linux or Windows host running pk95agent (push)
	TypeSNMP    = "snmp"    // switch, UPS, printer, NAS … polled over SNMP
	TypeNet     = "net"     // host or service checked only over the network (ping, HTTP, TCP, DNS, TLS)
	TypeNUT     = "nut"     // UPS polled over a NUT server (Network UPS Tools, e.g. UniFi UPS)
	TypeVeeam   = "veeam"   // Veeam Backup & Replication server, polled over its REST API
	TypePBS     = "pbs"     // Proxmox Backup Server, polled over its REST API
)

// Device is one monitored thing. Stored as JSON (store kind "device").
type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Host     string `json:"host"`  // hostname or IP; agents: filled from the first report
	Group    string `json:"group"` // free text for grouping and filtering (e.g. "Server", "Netzwerk")
	Notes    string `json:"notes"`
	Enabled  bool   `json:"enabled"`
	Interval int    `json:"interval"`  // seconds between polls; 0 = default of the type (see PollInterval)
	NoAlerts bool   `json:"no_alerts"` // states are shown, but no alerts/notifications are raised
	// AutoAccept monitors checks found after the first inventory at once instead of listing
	// them as new (undecided) checks.
	AutoAccept bool `json:"auto_accept,omitempty"`

	// ParentID attaches an agent device to another device (Proxmox, SNMP, net): the agent's
	// data complements the parent's API/SNMP data. Attached agents are shown inside the parent
	// (one entry in lists, parent state = worst of parent and attached agents).
	ParentID string `json:"parent_id,omitempty"`
	// Node is the part of the parent the agent belongs to, e.g. the Proxmox node name ("pve1");
	// empty for single-host parents.
	Node string `json:"node,omitempty"`

	Proxmox *ProxmoxConfig `json:"proxmox,omitempty"`
	SNMP    *SNMPConfig    `json:"snmp,omitempty"`
	NUT     *NUTConfig     `json:"nut,omitempty"`
	Veeam   *VeeamConfig   `json:"veeam,omitempty"`
	PBS     *PBSConfig     `json:"pbs,omitempty"`
	Agent   *AgentConfig   `json:"agent,omitempty"`

	// Checks are additional network checks; they work for every device type.
	Checks []NetCheck `json:"checks"`
	// Thresholds override the global defaults for this device.
	Thresholds []Threshold `json:"thresholds"`
	// Rules choose per check what is monitored: alert (default), quiet (shown, no alerts) or off.
	Rules []CheckRule `json:"rules"`
	// Maintenance mutes alerts until the given time.
	Maintenance *Maintenance `json:"maintenance,omitempty"`

	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

// Clone returns a deep copy, so callers can modify the result without touching shared state.
func (d Device) Clone() Device {
	if d.Proxmox != nil {
		p := *d.Proxmox
		p.IgnoreGuests = slices.Clone(p.IgnoreGuests)
		d.Proxmox = &p
	}
	if d.NUT != nil {
		n := *d.NUT
		d.NUT = &n
	}
	if d.Veeam != nil {
		v := *d.Veeam
		d.Veeam = &v
	}
	if d.PBS != nil {
		p := *d.PBS
		d.PBS = &p
	}
	if d.SNMP != nil {
		s := *d.SNMP
		s.Interfaces, s.OIDs = slices.Clone(s.Interfaces), slices.Clone(s.OIDs)
		d.SNMP = &s
	}
	if d.Agent != nil {
		a := *d.Agent
		a.IPs, a.Services, a.IgnoreServices = slices.Clone(a.IPs), slices.Clone(a.Services), slices.Clone(a.IgnoreServices)
		a.EventOff, a.EventRules, a.EventIgnore = slices.Clone(a.EventOff), CloneEventRules(a.EventRules), CloneEventIgnores(a.EventIgnore)
		d.Agent = &a
	}
	if d.Maintenance != nil {
		m := *d.Maintenance
		d.Maintenance = &m
	}
	d.Checks = slices.Clone(d.Checks)
	d.Thresholds = slices.Clone(d.Thresholds) // Warn/Crit pointers are never mutated in place
	d.Rules = slices.Clone(d.Rules)
	return d
}

// Check modes (CheckRule.Mode).
const (
	ModeAlert = "alert" // default: the state counts and raises alerts
	ModeQuiet = "quiet" // collected, stored and shown, but never warn/crit and never alerts
	ModeOff   = "off"   // hidden: no check, no metrics stored
	// ModeNew is not a rule mode: it is the mode of a check found after the device's first
	// inventory that was not decided yet (no rule matches, not accepted). It behaves like
	// ModeQuiet: collected, stored and shown, never warn/crit, never alerts.
	ModeNew = "new"
)

// DiscoveryKey is the check core synthesizes for the inventory: warn while there are new or
// vanished checks.
const DiscoveryKey = "discovery"

// ReachKey is the check core synthesizes for reachability; rules never apply to it.
const ReachKey = "reach"

// CheckRule sets the mode of every check whose key matches.
type CheckRule struct {
	// Match is a check key ("snmp:if:Gi1/0/1") or a pattern where * matches any text
	// ("snmp:if:*", "pve:vm:*", "disk:/boot*", "pve:updates:*").
	Match string `json:"match"`
	Mode  string `json:"mode"` // alert, quiet, off
}

// CheckMode returns the mode for a check key: an exact rule wins over patterns, among
// matching patterns the longest Match wins (the last one on a tie); default ModeAlert.
// The reach check is always ModeAlert.
func (d Device) CheckMode(key string) string {
	if key == ReachKey {
		return ModeAlert
	}
	mode, best := ModeAlert, -1
	for _, r := range d.Rules {
		if r.Match == key {
			return normMode(r.Mode)
		}
		if strings.Contains(r.Match, "*") && len(r.Match) >= best && GlobMatch(r.Match, key) {
			mode, best = normMode(r.Mode), len(r.Match)
		}
	}
	return mode
}

func normMode(m string) string {
	if m == ModeQuiet || m == ModeOff {
		return m
	}
	return ModeAlert
}

// HasRule reports whether a rule decides the mode of a check key (exact or pattern).
func (d Device) HasRule(key string) bool {
	for _, r := range d.Rules {
		if r.Match == key || (strings.Contains(r.Match, "*") && GlobMatch(r.Match, key)) {
			return true
		}
	}
	return false
}

// SeparateCheck reports whether a check has its own page instead of belonging to the device:
// update checks ("updates", "pve:updates:<node>"), the system logs ("ev:<rule>"; "eventlog" of
// agents before 0.16) and backups ("pve:backup", "pve:bak:<vmid>", "veeam:…", "pbs:…"). They raise
// alerts and notifications, but neither show on the device page nor colour the device, its
// folder or the overview, and the availability report leaves them out.
func SeparateCheck(key string) bool {
	return key == "updates" || key == "eventlog" || strings.HasPrefix(key, "pve:updates:") || strings.HasPrefix(key, "ev:") ||
		BackupCheck(key)
}

// BackupCheck reports whether a check is about backups (page "Sicherungen").
func BackupCheck(key string) bool {
	return key == "pve:backup" || strings.HasPrefix(key, "pve:bak:") || strings.HasPrefix(key, "veeam:") || strings.HasPrefix(key, "pbs:")
}

// CloneEventRules copies rules including their lists.
func CloneEventRules(list []EventRule) []EventRule {
	if list == nil {
		return nil
	}
	out := make([]EventRule, len(list))
	for i, r := range list {
		r.Logs, r.Sources, r.IDs, r.Levels = slices.Clone(r.Logs), slices.Clone(r.Sources), slices.Clone(r.IDs), slices.Clone(r.Levels)
		out[i] = r
	}
	return out
}

// CloneEventIgnores copies exceptions including their ID lists.
func CloneEventIgnores(list []EventIgnore) []EventIgnore {
	if list == nil {
		return nil
	}
	out := slices.Clone(list)
	for i := range out {
		out[i].IDs = slices.Clone(out[i].IDs)
	}
	return out
}

// GlobMatch reports whether s matches pattern, where * matches any sequence (also ":" and "/").
func GlobMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// PollInterval returns the poll (or expected report) interval in seconds.
func (d Device) PollInterval() int {
	if d.Interval > 0 {
		return d.Interval
	}
	return 60
}

// InMaintenance reports whether alerts are muted at time now (unix seconds).
func (d Device) InMaintenance(now int64) bool {
	return d.Maintenance != nil && d.Maintenance.Until > now
}

// Maintenance mutes a device until Until (unix seconds).
type Maintenance struct {
	Until int64  `json:"until"`
	Note  string `json:"note"`
	// Window is the maintenance window that set it and Start the start of its occurrence
	// (empty/0 for a maintenance set by hand).
	Window string `json:"window,omitempty"`
	Start  int64  `json:"start,omitempty"`
}

// ProxmoxConfig connects to the Proxmox VE API with an API token.
// The certificate is pinned by its SHA-256 fingerprint (empty = system CAs).
type ProxmoxConfig struct {
	Port        int    `json:"port"`     // default 8006
	TokenID     string `json:"token_id"` // e.g. root@pam!pk95mon
	Secret      string `json:"secret,omitempty"`
	Fingerprint string `json:"fingerprint"`
	// WatchGuests selects which guests must be running:
	// "onboot" (default: guests with "Start at boot"), "all", "none".
	WatchGuests string `json:"watch_guests"`
	// IgnoreGuests are VMIDs that never alert (e.g. templates or test VMs).
	IgnoreGuests []string `json:"ignore_guests"`
}

// SNMPConfig polls a device over SNMP v1, v2c or v3.
type SNMPConfig struct {
	Version   string `json:"version"` // "1", "2c" (default), "3"
	Port      int    `json:"port"`    // default 161
	Community string `json:"community,omitempty"`
	// v3 (USM)
	User      string `json:"user,omitempty"`
	SecLevel  string `json:"sec_level,omitempty"`  // noAuthNoPriv, authNoPriv, authPriv
	AuthProto string `json:"auth_proto,omitempty"` // MD5, SHA, SHA224, SHA256, SHA384, SHA512
	AuthPass  string `json:"auth_pass,omitempty"`
	PrivProto string `json:"priv_proto,omitempty"` // DES, AES, AES192, AES256
	PrivPass  string `json:"priv_pass,omitempty"`
	// Profile chooses the extra MIBs: "auto" (default, detected from sysObjectID/sysDescr),
	// "generic", "switch", "unifi" (UniFi switch: switch MIBs plus the Broadcom FASTPATH tree),
	// "ups", "printer", "nas".
	Profile string `json:"profile"`
	// Interfaces are ifName/ifDescr values whose link must be up (crit when not up,
	// "Alarm bei Link-Ausfall"). Every other non-loopback port is reported too and stays
	// ok without link. Traffic is recorded for ports that are up (at most 512 ports).
	Interfaces []string    `json:"interfaces"`
	OIDs       []CustomOID `json:"oids"`
}

// VeeamConfig polls Veeam Backup & Replication 12 or newer over its REST API (HTTPS, port 9419)
// with a user of the Veeam console (a Windows or Veeam user; the role "Restore Operator" or
// higher can read jobs and repositories).
type VeeamConfig struct {
	Port        int    `json:"port"` // default 9419
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	Fingerprint string `json:"fingerprint"` // SHA-256 of the server certificate (pinning); empty = system CAs
}

// PBSConfig polls a Proxmox Backup Server over its REST API (HTTPS, port 8007) with an API
// token that has the role Audit on / (Sys.Audit and Datastore.Audit).
type PBSConfig struct {
	Port        int    `json:"port"`     // default 8007
	TokenID     string `json:"token_id"` // e.g. pk95mon@pbs!monitoring
	Secret      string `json:"secret,omitempty"`
	Fingerprint string `json:"fingerprint"` // SHA-256 of the server certificate (pinning); empty = system CAs
}

// BackupServer reports whether a device type is a backup server (managed on the page
// "Sicherungen", not in the device list).
func BackupServer(typ string) bool { return typ == TypeVeeam || typ == TypePBS }

// NUTConfig polls a NUT server (upsd, TCP 3493): LIST UPS, LIST VAR.
type NUTConfig struct {
	Port     int    `json:"port"`     // default 3493
	UPS      string `json:"ups"`      // UPS name on the server; empty = every UPS the server lists
	Username string `json:"username"` // optional (only needed if upsd requires a login for reading)
	Password string `json:"password,omitempty"`
}

// CustomOID is an extra numeric value read by OID. It becomes metric "custom" with instance Name.
type CustomOID struct {
	OID   string  `json:"oid"`
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Scale float64 `json:"scale"` // multiplier, 0 = 1
}

// AgentConfig describes a pk95agent host. The token hash lives in the store, not here.
type AgentConfig struct {
	AgentID         string   `json:"agent_id"` // stable ID sent by the agent (machine ID)
	OS              string   `json:"os"`       // linux, windows, darwin
	Platform        string   `json:"platform"` // debian, ubuntu, Microsoft Windows Server 2022 Standard …
	PlatformVersion string   `json:"platform_version"`
	Kernel          string   `json:"kernel"`
	Arch            string   `json:"arch"`
	Hostname        string   `json:"hostname"`
	Version         string   `json:"version"` // agent version
	IPs             []string `json:"ips"`
	LastSeen        int64    `json:"last_seen"`
	// Services that must be running (Windows service names or systemd units). Entries with
	// "*" are patterns ("veeam*"): every matching service that runs or starts automatically.
	Services []string `json:"services"`
	// WatchAutoServices (Windows): every service with start type "automatic" must run.
	WatchAutoServices bool `json:"watch_auto_services"`
	// IgnoreServices are never reported, even if WatchAutoServices is on or a pattern (device or
	// central) matches; names or patterns.
	IgnoreServices []string `json:"ignore_services"`
	// Windows event log: EventOff lists the central rules switched off for this host,
	// EventRules are rules of this host only, EventIgnore drops events of this host.
	EventOff    []string      `json:"event_off"`
	EventRules  []EventRule   `json:"event_rules"`
	EventIgnore []EventIgnore `json:"event_ignore"`
}

// Event levels of the Windows event log (0 = LogAlways, e.g. security audit events).
const (
	EventCritical = 1
	EventError    = 2
	EventWarning  = 3
	EventInfo     = 4
)

// EventRule watches the system log of agents: the Windows event log or, with OS "linux", the
// systemd journal. The events of the last Hours that match every filled filter are counted;
// with Count or more the check "ev:<ID>" gets Severity.
type EventRule struct {
	ID      string `json:"id"` // built-in rules have word IDs ("disk"), own rules random ones
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	OS      string `json:"os,omitempty"` // windows (also when empty) or linux
	// Windows: event logs (System, Application, Security, Microsoft-Windows-Backup …).
	// Linux: "journal" (all messages) or "kernel" (only kernel messages).
	Logs []string `json:"logs"`
	// Windows: provider names. Linux: syslog identifiers (sshd, kernel) or systemd units
	// (nginx.service). "*" patterns; empty = any.
	Sources []string `json:"sources"`
	IDs     []int    `json:"ids"`    // Windows event IDs; empty = any
	Levels  []int    `json:"levels"` // Windows EventCritical …, Linux syslog priority 0–7; empty = any
	// Text: the message must contain this (case-insensitive); "|" separates alternatives.
	Text     string `json:"text,omitempty"`
	Count    int    `json:"count"`          // events needed for an alert, default 1
	Hours    int    `json:"hours"`          // time window, default 24
	Severity string `json:"severity"`       // warn or crit
	Hint     string `json:"hint,omitempty"` // what to do (German, shown with the check)
}

// EventIgnore drops matching events before any rule counts them: every filled field must
// match. Source is a provider name or "*" pattern, Text is contained in the message.
type EventIgnore struct {
	ID      string `json:"id"`
	Source  string `json:"source,omitempty"`
	IDs     []int  `json:"ids,omitempty"`
	Text    string `json:"text,omitempty"`
	Note    string `json:"note,omitempty"`
	Created int64  `json:"created,omitempty"`
}

// EventEntry is one event of the Windows event log.
type EventEntry struct {
	Time    int64  `json:"t"`              // unix seconds
	Log     string `json:"log"`            // Windows event log; Linux "kernel" or "journal"
	Source  string `json:"src"`            // provider (Windows) or syslog identifier (Linux)
	Unit    string `json:"unit,omitempty"` // Linux: systemd unit
	ID      int    `json:"id"`
	Level   int    `json:"lvl"`
	Message string `json:"msg,omitempty"`
}

// Network check types.
const (
	NetPing = "ping"
	NetHTTP = "http"
	NetTCP  = "tcp"
	NetDNS  = "dns"
	NetTLS  = "tls"
	// NetDomain asks the registry (RDAP) when the domain of the target expires.
	NetDomain = "domain"
)

// NetCheck is one network check of a device.
type NetCheck struct {
	ID     string `json:"id"`   // stable within the device (6 random chars); check key is "net:"+ID
	Type   string `json:"type"` // ping, http, tcp, dns, tls, domain
	Name   string `json:"name"`
	Target string `json:"target"` // ping: host (empty = device host); http: URL; tcp/tls: host:port; dns: name to resolve; domain: domain (empty = device host)
	// http
	Method       string `json:"method,omitempty"`        // GET (default) or HEAD
	ExpectStatus int    `json:"expect_status,omitempty"` // 0 = any 2xx/3xx
	Keyword      string `json:"keyword,omitempty"`       // body must contain this text
	IgnoreTLS    bool   `json:"ignore_tls,omitempty"`    // accept invalid certificates (still reports days left)
	// dns
	Server     string `json:"server,omitempty"`      // resolver host[:port]; empty = system resolver
	RecordType string `json:"record_type,omitempty"` // A (default), AAAA, CNAME, MX, TXT, NS
	Expect     string `json:"expect,omitempty"`      // one answer must equal this value
	Timeout    int    `json:"timeout,omitempty"`     // seconds, default 10
}

// Key returns the check key of a network check.
func (n NetCheck) Key() string { return "net:" + n.ID }

// Metric is one measured value of a check.
// Names and units are listed in DESIGN.md ("Metrics"); thresholds match on Name
// (and optionally Instance).
type Metric struct {
	Name     string  `json:"n"`           // e.g. "cpu.usage", "disk.usage"
	Instance string  `json:"i,omitempty"` // e.g. "/", "C:", "eth0", "vm/100", "pve1/local-lvm"
	Value    float64 `json:"v"`
	Unit     string  `json:"u,omitempty"` // %, B, B/s, ms, s, d, min, V, °C, W, "" (count)
}

// SeriesKey identifies the time series of a metric within a device.
func (m Metric) SeriesKey() string {
	if m.Instance == "" {
		return m.Name
	}
	return m.Name + "|" + m.Instance
}

// Check is the result of one check (one row in the UI). Collectors and the agent produce
// them; State is the native state of the collector, before thresholds are applied.
type Check struct {
	Key     string            `json:"key"`             // stable, see DESIGN.md ("Check keys")
	Name    string            `json:"name"`            // German label, e.g. "Festplatte /"
	Group   string            `json:"group,omitempty"` // UI section, e.g. "System", "Festplatten", "Dienste"
	State   string            `json:"state"`
	Message string            `json:"msg,omitempty"`
	Metrics []Metric          `json:"metrics,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"` // static info (OS, model, serial …), German labels as keys
	// Events are the latest matching events of a Windows event rule check ("ev:<id>").
	Events []EventEntry `json:"events,omitempty"`
}

// CheckStatus is a check with its effective state after thresholds and hysteresis.
type CheckStatus struct {
	Check
	Native  string `json:"native"`           // state reported by the collector
	Since   int64  `json:"since"`            // time of the last change of State
	Pending int    `json:"pending"`          // consecutive bad results not yet counted (hysteresis)
	Reason  string `json:"reason,omitempty"` // why State is what it is, e.g. "CPU-Auslastung 97 % ≥ 90 %"
	// Mode is the check mode when it is not ModeAlert (i.e. "quiet").
	Mode string `json:"mode,omitempty"`
	// Muted is the state a quiet check would have (warn/crit); its State is then ok.
	Muted string `json:"muted,omitempty"`
	// Acked: the open alert of this warn/crit check is acknowledged. The check keeps its
	// State, but counts as ok for the device until it gets worse (that clears the ack).
	Acked bool `json:"acked,omitempty"`
}

// HiddenCheck is a check switched off by a rule (listed so the UI can show it again).
type HiddenCheck struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

// DeviceStatus is the runtime state of a device (kept in memory, persisted in the store).
type DeviceStatus struct {
	DeviceID string        `json:"device_id"`
	State    string        `json:"state"`   // worst check state; crit when unreachable
	Message  string        `json:"message"` // short reason for the state
	Since    int64         `json:"since"`
	LastPoll int64         `json:"last_poll"` // last poll or agent report
	LastOK   int64         `json:"last_ok"`   // last successful poll or report
	Error    string        `json:"error,omitempty"`
	Checks   []CheckStatus `json:"checks"`
	Hidden   []HiddenCheck `json:"hidden,omitempty"` // checks switched off by a rule (last poll)
	// Vanished are checks of the inventory missing from the last collections.
	Vanished []VanishedCheck `json:"vanished,omitempty"`
	// Inventory is every check key the device has reported (discovery). It is persisted with
	// the status but left out of API responses.
	Inventory []InventoryItem `json:"inventory,omitempty"`
}

// InventoryItem is one check the device has reported since its first inventory.
type InventoryItem struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	First int64  `json:"first"` // first seen
	Last  int64  `json:"last"`  // last seen
	// New marks a check found after the first inventory without a decision yet (no rule
	// matched, not accepted): it runs in ModeNew.
	New bool `json:"new,omitempty"`
	// Missing counts the successful collections in a row without this check.
	Missing int `json:"missing,omitempty"`
}

// VanishedCheck is a check of the inventory missing from the last collections.
type VanishedCheck struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	Last  int64  `json:"last"` // last seen
}

// Threshold turns a metric value into a state.
type Threshold struct {
	// Metric is a metric name ("disk.usage") or name:instance ("disk.usage:/var").
	Metric string   `json:"metric"`
	Warn   *float64 `json:"warn,omitempty"`
	Crit   *float64 `json:"crit,omitempty"`
	// Below: the state is bad when the value is lower (battery, certificate days, toner).
	Below bool `json:"below,omitempty"`
	// Count is the number of consecutive bad values before the state changes (default 1).
	Count    int  `json:"count,omitempty"`
	Disabled bool `json:"disabled,omitempty"`
}

// Matches reports whether the threshold applies to the metric.
func (t Threshold) Matches(m Metric) bool {
	name, inst, hasInst := strings.Cut(t.Metric, ":")
	if name != m.Name {
		return false
	}
	return !hasInst || inst == m.Instance
}

// Specific reports whether the threshold names an instance (wins over a plain name).
func (t Threshold) Specific() bool { return strings.Contains(t.Metric, ":") }

func f(v float64) *float64 { return &v }

// DefaultThresholds are used unless the settings or a device override them.
func DefaultThresholds() []Threshold {
	return []Threshold{
		{Metric: "cpu.usage", Warn: f(90), Crit: f(98), Count: 5},
		{Metric: "mem.usage", Warn: f(90), Crit: f(97), Count: 3},
		{Metric: "disk.usage", Warn: f(85), Crit: f(95)},
		{Metric: "storage.usage", Warn: f(85), Crit: f(95)},
		{Metric: "ping.loss", Warn: f(20), Crit: f(100), Count: 2},
		{Metric: "http.time", Warn: f(5000), Crit: f(15000), Count: 2},
		{Metric: "cert.days", Warn: f(21), Crit: f(7), Below: true},
		{Metric: "domain.days", Warn: f(30), Crit: f(14), Below: true},
		{Metric: "ups.battery", Warn: f(50), Crit: f(20), Below: true},
		{Metric: "ups.runtime", Warn: f(10), Crit: f(5), Below: true},
		{Metric: "printer.toner", Warn: f(10), Crit: f(2), Below: true},
		{Metric: "poe.usage", Warn: f(85), Crit: f(95)},
		{Metric: "updates.security", Warn: f(1)},
		{Metric: "updates.age", Warn: f(35), Crit: f(60)},
		{Metric: "updates.failed", Warn: f(1)},
		// a daily backup older than a day and a half (warn) or two days and more (crit)
		{Metric: "backup.age", Warn: f(30), Crit: f(54)},
		// Proxmox hardware and storage health
		{Metric: "cpu.iowait", Warn: f(10), Crit: f(25), Count: 5},
		{Metric: "disk.temp", Warn: f(60), Crit: f(70), Count: 2},
		{Metric: "disk.wearout", Warn: f(90), Crit: f(98)},
		{Metric: "disk.spare", Warn: f(20), Crit: f(10), Below: true},
		{Metric: "disk.pending", Warn: f(1), Crit: f(10)},
		{Metric: "disk.reallocated", Warn: f(1)},
		{Metric: "disk.uncorrectable", Warn: f(1)},
		{Metric: "disk.media_errors", Warn: f(1)},
		{Metric: "zfs.usage", Warn: f(80), Crit: f(90)},
		{Metric: "zfs.errors", Warn: f(1)},
		{Metric: "zfs.scrub_age", Warn: f(35)},
	}
}

// Alert is one period during which a check was bad (warn or crit).
type Alert struct {
	ID         int64  `json:"id"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	CheckKey   string `json:"check_key"`
	CheckName  string `json:"check_name"`
	Severity   string `json:"severity"` // warn or crit (the worst state seen while open)
	Message    string `json:"message"`
	Started    int64  `json:"started"`
	Ended      int64  `json:"ended,omitempty"` // 0 = still active
	AckedBy    string `json:"acked_by,omitempty"`
	AckedAt    int64  `json:"acked_at,omitempty"`
}

// ---- agent protocol (HTTPS, JSON) ----

// AgentRegister is sent once by "pk95agent install" to POST /agent/v1/register.
type AgentRegister struct {
	Key     string `json:"key"` // enrollment key shown in the web UI
	AgentID string `json:"agent_id"`
	// DeviceID (optional, from "pk95agent install --device") attaches the agent to that
	// existing device (see Device.ParentID); Node optionally names the part, e.g. the PVE node.
	DeviceID        string   `json:"device_id,omitempty"`
	Node            string   `json:"node,omitempty"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Platform        string   `json:"platform"`
	PlatformVersion string   `json:"platform_version"`
	Kernel          string   `json:"kernel"`
	Arch            string   `json:"arch"`
	Version         string   `json:"version"`
	IPs             []string `json:"ips"`
}

// AgentRegistered is the answer to AgentRegister. Token authenticates all later requests
// (Authorization: Bearer <token>).
type AgentRegistered struct {
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
	Interval int    `json:"interval"`
}

// AgentReport is sent every interval to POST /agent/v1/report. Reports that could not be
// delivered are buffered by the agent and sent later with their original Time.
type AgentReport struct {
	AgentID         string   `json:"agent_id"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Platform        string   `json:"platform"`
	PlatformVersion string   `json:"platform_version"`
	Kernel          string   `json:"kernel"`
	Arch            string   `json:"arch"`
	Version         string   `json:"version"`
	IPs             []string `json:"ips"`
	Uptime          int64    `json:"uptime"` // seconds
	Time            int64    `json:"time"`   // unix seconds when collected
	Buffered        bool     `json:"buffered,omitempty"`
	Checks          []Check  `json:"checks"`
}

// AgentReply is the answer to a report: the configuration the agent should use next.
type AgentReply struct {
	Interval          int      `json:"interval"`
	Services          []string `json:"services"`
	WatchAutoServices bool     `json:"watch_auto_services"`
	IgnoreServices    []string `json:"ignore_services"`
	// CentralServices are the services of the central setting for the agent's OS (names or
	// patterns like "veeam*"): monitored where present, when they run or start automatically.
	CentralServices []string `json:"central_services"`
	// EventRules are the effective Windows event rules of the host (central ones not switched
	// off plus its own, enabled only); EventIgnore the central and the host's exceptions.
	EventRules  []EventRule   `json:"event_rules"`
	EventIgnore []EventIgnore `json:"event_ignore"`
	Update      *AgentUpdate  `json:"update,omitempty"`
}

// AgentUpdate tells the agent to replace itself with another version.
type AgentUpdate struct {
	Version string `json:"version"`
	URL     string `json:"url"` // path on the server, e.g. /agent/download/linux/amd64
	SHA256  string `json:"sha256"`
}

// ---- external API (for a central instance) ----

// Summary is served at GET /api/v1/summary and pushed to the central instance.
type Summary struct {
	Instance string          `json:"instance"` // name of this installation (customer)
	Version  string          `json:"version"`
	URL      string          `json:"url"`
	Time     int64           `json:"time"`
	State    string          `json:"state"`  // worst device state
	Counts   map[string]int  `json:"counts"` // ok, warn, crit, unknown, maintenance, disabled
	Devices  []DeviceSummary `json:"devices"`
	Alerts   []Alert         `json:"alerts"` // active alerts
}

// DeviceSummary is one device in the external API.
type DeviceSummary struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Group       string         `json:"group"`
	Host        string         `json:"host"`
	Enabled     bool           `json:"enabled"`
	Maintenance bool           `json:"maintenance"`
	State       string         `json:"state"`
	Message     string         `json:"message"`
	LastSeen    int64          `json:"last_seen"`
	Checks      map[string]int `json:"checks"` // count per state
}
