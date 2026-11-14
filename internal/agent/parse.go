package agent

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"unicode/utf16"
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

// eventlogResult is printed by the Windows event log script.
type eventlogResult struct {
	Errors   int    `json:"errors"`   // Error and Critical in System+Application, last 24 h
	Critical int    `json:"critical"` // Critical in the last hour
	Last     string `json:"last"`     // newest critical event: "Provider: first line"
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
