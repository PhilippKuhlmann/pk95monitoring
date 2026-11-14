package agent

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
)

// Pure parsers for the output of system tools. They live in an untagged file so their tests
// run on every OS.

// updateCount is the result of an update search.
type updateCount struct {
	Pending  int `json:"pending"`
	Security int `json:"security"`
	// Windows only: titles of pending updates (at most 10), the time of the last successful
	// installation (unix seconds, definition updates excluded; 0 = unknown) and the updates
	// whose last installation attempt in the past 30 days failed.
	Titles      []string `json:"titles"`
	LastInstall int64    `json:"last_install"`
	Failed      int      `json:"failed"`
}

// parseAptUpgrade counts the "Inst " lines of "apt-get -s upgrade"; security updates come
// from a "-security" suite (Debian) or a "Security" origin (Ubuntu, Debian-Security).
func parseAptUpgrade(out string) updateCount {
	var c updateCount
	for line := range strings.Lines(out) {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		c.Pending++
		if strings.Contains(line, "-security") || strings.Contains(line, "Security") {
			c.Security++
		}
	}
	return c
}

// parseDnfCheckUpdate counts the package lines of "dnf -q check-update" ("name.arch version
// repo"). Long names may wrap onto a second line; the "Obsoleting Packages" section is not
// counted (those packages are already listed above).
func parseDnfCheckUpdate(out string) int {
	n := 0
	var pending []string
	for line := range strings.Lines(out) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Obsoleting") {
			break
		}
		f := strings.Fields(trimmed)
		if len(f) == 0 || strings.HasPrefix(line, " ") && len(pending) == 0 {
			continue
		}
		f = append(pending, f...)
		pending = nil
		switch {
		case len(f) == 1 && strings.Contains(f[0], "."):
			pending = f // wrapped: version and repo follow on the next line
		case len(f) == 3 && strings.Contains(f[0], ".") && !strings.HasSuffix(f[0], ":"):
			n++
		}
	}
	return n
}

var rpmArchSuffixes = []string{".x86_64", ".noarch", ".aarch64", ".i686", ".ppc64le", ".s390x"}

// parseDnfSecurity counts the distinct packages in "dnf updateinfo list --security"
// (dnf4: "ID Severity/Sec. package", dnf5: "ID security Severity package date").
func parseDnfSecurity(out string) int {
	seen := map[string]bool{}
	for line := range strings.Lines(out) {
		for _, f := range strings.Fields(line) {
			for _, a := range rpmArchSuffixes {
				if strings.HasSuffix(f, a) {
					seen[f] = true
				}
			}
		}
	}
	return len(seen)
}

// parseFailedUnits returns the unit names of
// "systemctl list-units --state=failed --no-legend --plain".
func parseFailedUnits(out string) []string {
	var units []string
	for line := range strings.Lines(out) {
		f := strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "●*× "))
		if len(f) == 0 || !strings.Contains(f[0], ".") {
			continue
		}
		units = append(units, f[0])
	}
	return units
}

// parseIsActive maps the units passed to "systemctl is-active u1 u2 …" to the state printed
// for each (one line per unit, in order). Missing lines become "unknown".
func parseIsActive(units []string, out string) map[string]string {
	var states []string
	for line := range strings.Lines(out) {
		if s := strings.TrimSpace(line); s != "" {
			states = append(states, s)
		}
	}
	m := make(map[string]string, len(units))
	for i, u := range units {
		m[u] = "unknown"
		if i < len(states) {
			m[u] = states[i]
		}
	}
	return m
}

// unitInfo is a loaded service unit of "systemctl list-units".
type unitInfo struct {
	Active      string // ActiveState: active, inactive, failed, activating …
	Description string
}

// parseListUnits reads "systemctl list-units --type=service --all --no-legend --plain"
// (UNIT LOAD ACTIVE SUB DESCRIPTION) into unit → info; units that are not found are skipped.
func parseListUnits(out string) map[string]unitInfo {
	m := map[string]unitInfo{}
	for line := range strings.Lines(out) {
		f := strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "●*× "))
		if len(f) < 4 || !strings.HasSuffix(f[0], ".service") || f[1] == "not-found" {
			continue
		}
		m[f[0]] = unitInfo{Active: f[2], Description: strings.Join(f[4:], " ")}
	}
	return m
}

// parseEnabledUnits reads "systemctl list-unit-files --type=service --no-legend" (UNIT STATE
// [PRESET]) and returns the units that start automatically (enabled); templates are skipped.
func parseEnabledUnits(out string) map[string]bool {
	m := map[string]bool{}
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasSuffix(f[0], ".service") || strings.HasSuffix(f[0], "@.service") {
			continue
		}
		if f[1] == "enabled" || f[1] == "enabled-runtime" {
			m[f[0]] = true
		}
	}
	return m
}

// unitStateText translates systemd's ActiveState for messages.
func unitStateText(s string) string {
	switch s {
	case "active":
		return "läuft"
	case "inactive":
		return "gestoppt"
	case "failed":
		return "fehlgeschlagen"
	case "activating":
		return "startet"
	case "deactivating":
		return "wird beendet"
	case "unknown":
		return "nicht gefunden"
	}
	return s
}

// psList decodes a JSON array that PowerShell may print as a single object (or null).
type psList[T any] []T

func (l *psList[T]) UnmarshalJSON(b []byte) error {
	switch s := strings.TrimSpace(string(b)); {
	case s == "null":
		*l = nil
		return nil
	case strings.HasPrefix(s, "{"):
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*l = []T{v}
		return nil
	}
	var list []T
	err := json.Unmarshal(b, &list)
	*l = list
	return err
}

// journalLine is one entry of "journalctl -o json".
type journalLine struct {
	Time       string          `json:"__REALTIME_TIMESTAMP"` // microseconds
	Priority   string          `json:"PRIORITY"`
	Identifier string          `json:"SYSLOG_IDENTIFIER"`
	Comm       string          `json:"_COMM"`
	Unit       string          `json:"_SYSTEMD_UNIT"`
	AboutUnit  string          `json:"UNIT"` // systemd's messages about a unit
	Transport  string          `json:"_TRANSPORT"`
	Message    json.RawMessage `json:"MESSAGE"` // a string, or an array of bytes for binary data
}

// parseJournal decodes the output of "journalctl -o json" (one object per line). Lines that
// are not JSON (e.g. "-- No entries --") are skipped.
func parseJournal(out string) []model.EventEntry {
	var list []model.EventEntry
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var j journalLine
		if json.Unmarshal([]byte(line), &j) != nil {
			continue
		}
		us, err := strconv.ParseInt(j.Time, 10, 64)
		if err != nil {
			continue
		}
		e := model.EventEntry{Time: us / 1_000_000, Log: eventrules.LogJournal, Source: j.Identifier, Unit: j.Unit, Level: 6}
		if j.Transport == "kernel" {
			e.Log = eventrules.LogKernel
			if e.Source == "" {
				e.Source = "kernel"
			}
		}
		if e.Source == "" {
			e.Source = j.Comm
		}
		if j.AboutUnit != "" {
			e.Unit = j.AboutUnit
		}
		if p, err := strconv.Atoi(j.Priority); err == nil && p >= 0 && p <= 7 {
			e.Level = p
		}
		var msg string
		if json.Unmarshal(j.Message, &msg) != nil {
			var ints []int
			var raw []byte
			if json.Unmarshal(j.Message, &ints) == nil {
				for _, v := range ints {
					raw = append(raw, byte(v))
				}
			}
			msg = strings.ToValidUTF8(string(raw), "?")
		}
		e.Message = truncate(msg, 1000)
		list = append(list, e)
	}
	return list
}

// parseEventlog decodes the output of the Windows event log script: one result per rule.
func parseEventlog(out string) (map[string]eventrules.Result, error) {
	var doc struct {
		Rules psList[struct {
			ID     string                   `json:"id"`
			Events psList[model.EventEntry] `json:"events"`
			Full   bool                     `json:"full"`
			Err    string                   `json:"err"`
		}] `json:"rules"`
	}
	if err := parsePSJSON(out, &doc); err != nil {
		return nil, err
	}
	res := make(map[string]eventrules.Result, len(doc.Rules))
	for _, r := range doc.Rules {
		res[r.ID] = eventrules.Result{Events: r.Events, Full: r.Full, Err: r.Err}
	}
	return res, nil
}

// parsePSJSON decodes the JSON object a PowerShell script printed (ignoring noise around it).
func parsePSJSON(out string, v any) error {
	s := strings.TrimSpace(out)
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndexByte(s, '}'); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	return json.Unmarshal([]byte(s), v)
}

// encodePS encodes a script for "powershell.exe -EncodedCommand" (base64 of UTF-16LE), which
// avoids every quoting problem of -Command.
func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[2*i:], r)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// truncate shortens s to n runes, adding "…".
func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
