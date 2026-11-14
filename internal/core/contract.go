// Package core is the monitoring engine: device registry, scheduler, threshold evaluation
// with hysteresis, alerts and notifications, agent ingestion, metric rollup/retention,
// the summary for the external API and the push to a central instance.
//
// This file is the contract between core and internal/api: the exported types below are
// fixed. The methods live in the other files of this package (core.go, devices.go,
// eval.go, status.go, scheduler.go, agents.go, views.go, central.go).
package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"pk95monitoring/internal/agentdist"
	"pk95monitoring/internal/collect/nut"
	"pk95monitoring/internal/collect/proxmox"
	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

// Errors returned to the API.
var (
	ErrBadEnrollKey = errors.New("Registrierungsschlüssel ungültig")
	ErrUnauthorized = errors.New("Agent-Token ungültig")
)

// Options configure the engine (from the environment, see DESIGN.md).
type Options struct {
	Version   string
	DataDir   string
	AgentDir  string // directory with the agent binaries
	PublicURL string // PK95MON_PUBLIC_URL (overrides the setting when not empty)
	Instance  string // PK95MON_INSTANCE (overrides the setting when not empty)
	Demo      bool
}

// GeneralSettings are the "Allgemein" settings.
type GeneralSettings struct {
	Instance         string `json:"instance"`
	PublicURL        string `json:"public_url"`
	RetentionRawDays int    `json:"retention_raw_days"` // default store.DefaultRawDays
	Retention5mDays  int    `json:"retention_5m_days"`  // default store.DefaultFiveDays
	Retention1hDays  int    `json:"retention_1h_days"`  // default store.DefaultHourDays
	RenotifyHours    int    `json:"renotify_hours"`     // 0 = never
}

// CentralSettings configure the push of the summary to a central instance.
type CentralSettings struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
	Interval int    `json:"interval"` // minutes, default 5
}

// AgentSettings configure agent distribution.
type AgentSettings struct {
	AutoUpdate bool `json:"auto_update"` // default true
	// WindowsServices and LinuxServices are monitored on every agent of that OS where present:
	// names or patterns ("veeam*"), each matching service that runs or starts automatically.
	WindowsServices []string `json:"windows_services"`
	LinuxServices   []string `json:"linux_services"`
	// EventRules are the changed built-in and the own central Windows event rules (see
	// eventrules.Merge); EventIgnore the exceptions for all Windows agents.
	EventRules  []model.EventRule   `json:"event_rules"`
	EventIgnore []model.EventIgnore `json:"event_ignore"`
}

// centralServices returns the central service list for an agent OS.
func (a AgentSettings) centralServices(os string) []string {
	switch os {
	case "windows":
		return a.WindowsServices
	case "linux":
		return a.LinuxServices
	}
	return nil
}

// Settings are all persisted settings. Secrets (notify passwords/tokens, central token) are
// returned in full by Settings(); the API redacts them.
type Settings struct {
	General    GeneralSettings   `json:"general"`
	Notify     notify.Settings   `json:"notify"`
	Thresholds []model.Threshold `json:"thresholds"` // global overrides of model.DefaultThresholds
	Central    CentralSettings   `json:"central"`
	Report     ReportSettings    `json:"report"`
	Agent      AgentSettings     `json:"agent"`
}

// Counts per device state.
type Counts struct {
	OK          int `json:"ok"`
	Warn        int `json:"warn"`
	Crit        int `json:"crit"`
	Unknown     int `json:"unknown"`
	Maintenance int `json:"maintenance"`
	Disabled    int `json:"disabled"`
}

// CheckCounts counts the checks of a device per state.
type CheckCounts struct {
	OK      int `json:"ok"`
	Warn    int `json:"warn"`
	Crit    int `json:"crit"`
	Unknown int `json:"unknown"`
	Acked   int `json:"acked,omitempty"` // acknowledged problems (counted as OK)
}

// DeviceRow is one device in lists (overview, devices, agents). See DESIGN.md.
type DeviceRow struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Type             string      `json:"type"`
	Host             string      `json:"host"`
	Group            string      `json:"group"`
	Enabled          bool        `json:"enabled"`
	NoAlerts         bool        `json:"no_alerts"`
	MaintenanceUntil int64       `json:"maintenance_until"`
	State            string      `json:"state"`
	Message          string      `json:"message"`
	Since            int64       `json:"since"`
	LastPoll         int64       `json:"last_poll"`
	OS               string      `json:"os"`
	Platform         string      `json:"platform"`
	AgentVersion     string      `json:"agent_version"`
	Checks           CheckCounts `json:"checks"`
	CPU              *float64    `json:"cpu"`
	Mem              *float64    `json:"mem"`
	Disk             *float64    `json:"disk"`
	// ParentID and ParentName are set for an agent attached to another device (only in
	// lists that show attached agents, e.g. the Agents page).
	ParentID   string `json:"parent_id,omitempty"`
	ParentName string `json:"parent_name,omitempty"`
	// Attached is the number of agents attached to the device; State, Message and Checks
	// then include them.
	Attached int `json:"attached"`
}

// Suggestion is a device an unattached agent probably belongs to (Core.Suggest).
type Suggestion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Node string `json:"node"` // Proxmox node matching the agent's host name, else ""
}

// Overview is the dashboard.
type Overview struct {
	Counts  Counts        `json:"counts"`
	Devices []DeviceRow   `json:"devices"`
	Alerts  []model.Alert `json:"alerts"`
	Events  []store.Event `json:"events"`
	Groups  []string      `json:"groups"`
}

// Core is the engine. Fields are set by New; collectors may be replaced (demo, tests)
// before Run is called.
type Core struct {
	St      *store.Store
	Opt     Options
	Started int64
	Agents  *agentdist.Catalog
	Proxmox *proxmox.Collector
	SNMP    *snmp.Collector
	NUT     *nut.Collector

	// Notify delivers a notification (default notify.Send); tests replace it.
	Notify func(ctx context.Context, s notify.Settings, m notify.Message) error
	// Mail sends a mail to the given addresses (default notify.SendMail, the monthly report); tests replace it.
	Mail func(s notify.Settings, m notify.Message, to []string) error
	// Now is the clock (default time.Now); tests replace it before use.
	Now func() time.Time

	smu      sync.RWMutex // guards settings and enrollKey; may be taken while holding mu
	settings Settings
	enroll   string

	mu      sync.Mutex // guards devices, rt, open, windows and winActive
	devices map[string]model.Device
	rt      map[string]*devState
	open    map[string]*openAlert // by alertKey(device, check)
	windows map[string]MaintWindow
	nrules  map[string]NotifyRule // notification rules
	digests map[string]int64      // rule ID → end of the last summarised quiet time
	// winActive is the start of the running occurrence per window (for the begin/end events).
	winActive map[string]int64

	// pmu serializes the store writes of device data that happen outside mu (statuses,
	// metrics, LastSeen) against DeleteDevice, so a deleted device leaves nothing behind.
	// Never acquired while holding mu.
	pmu sync.Mutex

	wake         chan struct{}
	tm           timings
	notifyFail   atomic.Bool // a notification failed and the warning event was written
	notifyWG     sync.WaitGroup
	centralFail  atomic.Bool
	centralNext  time.Time   // next central push (Run goroutine only)
	centralReset atomic.Bool // central settings changed: push at the next tick
	runStart     atomic.Int64
	silenceTick  atomic.Int64 // time of the last silence check (detects a suspended/paused server)
	running      sync.WaitGroup
}
