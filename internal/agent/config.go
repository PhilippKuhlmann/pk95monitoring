// Package agent is the runtime of pk95agent: it collects checks on the local host, pushes
// them to the PK95 Monitoring server over HTTPS, buffers undelivered reports and updates
// itself when the server offers a new version.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// Version of the agent. cmd/pk95agent sets it from main.version (-ldflags "-X main.version=…").
var Version = "dev"

// Service identity (systemd unit / Windows service).
const (
	ServiceName        = "pk95agent"
	ServiceDisplayName = "PK95 Monitoring Agent"
	ServiceDescription = "Sendet Zustand und Messwerte dieses Hosts an PK95 Monitoring."
)

// Interval bounds in seconds.
const (
	DefaultInterval = 60
	MinInterval     = 10
	MaxInterval     = 3600
)

// Config is the agent configuration file (JSON, mode 0600: it holds the token).
type Config struct {
	Server      string `json:"server"` // e.g. https://monitor.example:8443
	Token       string `json:"token"`
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint,omitempty"` // SHA-256 of the server certificate (pinning)
	Insecure    bool   `json:"insecure,omitempty"`    // skip certificate verification without pinning
	Interval    int    `json:"interval"`              // seconds between reports
	// Services must be running (systemd units or Windows service names); entries with "*"
	// are patterns: every matching service that runs or starts automatically.
	Services []string `json:"services"`
	// WatchAutoServices (Windows): every automatic-start service must run.
	WatchAutoServices bool     `json:"watch_auto_services"`
	IgnoreServices    []string `json:"ignore_services"`
	// CentralServices come from the server's central setting: names or patterns, monitored
	// where present (like patterns).
	CentralServices []string `json:"central_services,omitempty"`
	// EventRules and EventIgnore (Windows) come from the server: the event log rules of this
	// host and the exceptions that drop events before the rules count them.
	EventRules  []model.EventRule   `json:"event_rules,omitempty"`
	EventIgnore []model.EventIgnore `json:"event_ignore,omitempty"`
}

// IntervalSeconds returns the report interval clamped to sane bounds.
func (c Config) IntervalSeconds() int {
	switch {
	case c.Interval <= 0:
		return DefaultInterval
	case c.Interval < MinInterval:
		return MinInterval
	case c.Interval > MaxInterval:
		return MaxInterval
	}
	return c.Interval
}

// DefaultConfigPath returns the per-OS location of agent.json.
func DefaultConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "PK95Agent", "agent.json")
	case "darwin":
		return "/usr/local/etc/pk95agent/agent.json"
	default:
		return "/etc/pk95agent/agent.json"
	}
}

// BufferPath is the file of undelivered reports, next to the config.
func BufferPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "buffer.jsonl")
}

// Load reads and validates the config file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server == "" || c.Token == "" {
		return c, fmt.Errorf("%s: server oder token fehlt – Agent mit „pk95agent install“ neu registrieren", path)
	}
	return c, nil
}

// Save writes the config atomically (temp file + rename) with mode 0600 in a 0700 directory.
func Save(path string, c Config) error {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Restrict access to the directory (Windows ACL) when it is ours: created right now or the
	// default location. An existing directory given with --config is left alone.
	if errors.Is(statErr, os.ErrNotExist) || sameDir(dir, filepath.Dir(DefaultConfigPath())) {
		if err := secureDir(dir); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// applyReply merges the server's configuration into c and reports whether anything changed.
func (c *Config) applyReply(r model.AgentReply) bool {
	changed := false
	if r.Interval > 0 && r.Interval != c.Interval {
		c.Interval, changed = r.Interval, true
	}
	list := func(dst *[]string, src []string) {
		if !slices.Equal(src, *dst) && !(len(src) == 0 && len(*dst) == 0) {
			*dst, changed = slices.Clone(src), true
		}
	}
	list(&c.Services, r.Services)
	if r.WatchAutoServices != c.WatchAutoServices {
		c.WatchAutoServices, changed = r.WatchAutoServices, true
	}
	list(&c.IgnoreServices, r.IgnoreServices)
	list(&c.CentralServices, r.CentralServices)
	if !sameJSON(c.EventRules, r.EventRules) {
		c.EventRules, changed = model.CloneEventRules(r.EventRules), true
	}
	if !sameJSON(c.EventIgnore, r.EventIgnore) {
		c.EventIgnore, changed = slices.Clone(r.EventIgnore), true
	}
	return changed
}

// sameJSON compares two lists by their JSON form; nil and empty are the same.
func sameJSON[T any](a, b []T) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// MachineID returns a stable ID of this host: /etc/machine-id on Linux, MachineGuid on
// Windows, otherwise a hash of the hostname.
func MachineID() string {
	if id := strings.TrimSpace(platformMachineID()); id != "" {
		return strings.ToLower(id)
	}
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(strings.ToLower(host)))
	return hex.EncodeToString(sum[:16])
}
