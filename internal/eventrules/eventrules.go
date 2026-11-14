// Package eventrules turns system log entries – the Windows event log and the systemd journal
// on Linux – into checks. Rules say which events count
// (log, source, ID, level, text) and how many of them within some hours raise an alert;
// exceptions drop events every host keeps logging. The agent queries the event log with the
// filters of the rules and evaluates them here; the server validates and merges the rules.
// The package is pure Go, so it runs (and is tested) on every OS.
package eventrules

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// KeyPrefix starts the check key of an event rule ("ev:disk").
const KeyPrefix = "ev:"

// Group is the UI section of the event checks.
const Group = "Ereignisse"

// MetricCount is the number of matching events in the window of a rule.
const MetricCount = "events.count"

// Limits of rules and exceptions.
const (
	MaxRules       = 50 // per list (central, per device)
	MaxIgnores     = 200
	MaxLogs        = 5
	MaxSources     = 20
	MaxIDs         = 20 // Get-WinEvent fails with more than about 22 IDs in one filter
	MaxText        = 200
	MaxCount       = 500
	MaxHours       = 720
	MaxEvents      = 10  // events kept per check
	maxQueryEvents = 500 // events read per rule and log
)

// Operating systems of rules (EventRule.OS; empty = windows).
const (
	OSWindows = "windows"
	OSLinux   = "linux"
)

// Linux logs: the whole journal or only kernel messages.
const (
	LogJournal = "journal"
	LogKernel  = "kernel"
)

// LevelNames are the German names of the Windows event levels.
var LevelNames = map[int]string{0: "Überwachung", 1: "Kritisch", 2: "Fehler", 3: "Warnung", 4: "Information", 5: "Ausführlich"}

// LinuxLevelNames are the German names of the syslog priorities.
var LinuxLevelNames = map[int]string{0: "Notfall", 1: "Alarm", 2: "Kritisch", 3: "Fehler", 4: "Warnung", 5: "Hinweis", 6: "Info", 7: "Debug"}

// RuleOS is the operating system of a rule (windows when empty).
func RuleOS(r model.EventRule) string {
	if r.OS == OSLinux {
		return OSLinux
	}
	return OSWindows
}

// ForOS keeps the rules of one operating system.
func ForOS(list []model.EventRule, os string) []model.EventRule {
	out := []model.EventRule{}
	for _, r := range list {
		if RuleOS(r) == os {
			out = append(out, r)
		}
	}
	return out
}

func levelNames(os string) map[int]string {
	if os == OSLinux {
		return LinuxLevelNames
	}
	return LevelNames
}

// Defaults are the built-in rules: events that mean something on any Windows or Linux server.
// The catch-all rules for every error are off by default – no log is ever free of errors.
func Defaults() []model.EventRule {
	rules := []model.EventRule{
		{ID: "disk", Name: "Festplattenfehler", Enabled: true, Logs: []string{"System"},
			Sources: []string{"disk", "Ntfs", "Microsoft-Windows-Ntfs", "volmgr", "stornvme", "storahci", "iaStorA", "iaStorAVC", "megasas", "megasas2i"},
			IDs:     []int{7, 11, 15, 51, 52, 55, 98, 153}, Levels: []int{1, 2, 3}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Datenträger und Controller prüfen (SMART, RAID-Status) und sicherstellen, dass die Datensicherung aktuell ist. Bei NTFS-Fehlern chkdsk einplanen."},
		{ID: "bugcheck", Name: "Bluescreen", Enabled: true, Logs: []string{"System"},
			Sources: []string{"Microsoft-Windows-WER-SystemErrorReporting"}, IDs: []int{1001}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Absturzabbild (C:\\Windows\\Minidump) auswerten, Treiber, Firmware und BIOS aktualisieren."},
		{ID: "unexpected_shutdown", Name: "Unerwartetes Herunterfahren", Enabled: true, Logs: []string{"System"},
			Sources: []string{"Microsoft-Windows-Kernel-Power", "EventLog"}, IDs: []int{41, 6008}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Stromversorgung und USV prüfen, nach Bluescreens und Hardwarefehlern suchen; bei einer VM das Protokoll des Hosts ansehen."},
		{ID: "hardware", Name: "Hardwarefehler (WHEA)", Enabled: true, Logs: []string{"System"},
			Sources: []string{"Microsoft-Windows-WHEA-Logger"}, Levels: []int{1, 2}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Nicht behebbarer Hardwarefehler: Arbeitsspeicher, CPU und PCIe-Karten prüfen, BIOS und Firmware aktualisieren."},
		{ID: "backup", Name: "Windows-Server-Sicherung", Enabled: true, Logs: []string{"Microsoft-Windows-Backup"},
			Levels: []int{1, 2}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Sicherung in der Windows-Server-Sicherung (wbadmin) prüfen: Sicherungsziel erreichbar und groß genug, VSS-Fehler im Anwendungsprotokoll."},
		{ID: "veeam", Name: "Veeam-Sicherung", Enabled: true, Logs: []string{"Veeam Backup", "Veeam Agent"},
			IDs: []int{190}, Levels: []int{2}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Sitzungsprotokoll des Jobs in der Veeam-Konsole prüfen und den Job neu starten."},
		{ID: "service_crash", Name: "Dienst abgestürzt", Enabled: true, Logs: []string{"System"},
			Sources: []string{"Service Control Manager"}, IDs: []int{7031, 7034}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Welcher Dienst betroffen ist, steht in der Meldung. Im Anwendungsprotokoll zur gleichen Zeit nach der Ursache suchen."},
		{ID: "failed_logons", Name: "Fehlgeschlagene Anmeldungen", Enabled: true, Logs: []string{"Security"},
			Sources: []string{"Microsoft-Windows-Security-Auditing"}, IDs: []int{4625}, Count: 20, Hours: 1, Severity: model.StateWarn,
			Hint: "Viele Fehlversuche deuten auf Kennwort-Raten hin: Quelle (Arbeitsstation, IP-Adresse) in der Meldung prüfen, offenes RDP schließen, Kontosperrung einrichten."},
		{ID: "lockout", Name: "Konto gesperrt", Enabled: true, Logs: []string{"Security"},
			Sources: []string{"Microsoft-Windows-Security-Auditing"}, IDs: []int{4740}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Gesperrtes Konto und aufrufenden Computer in der Meldung prüfen – oft ein altes Kennwort in einem Dienst, Laufwerk oder Handy."},
		{ID: "dfsr", Name: "DFS-Replikation", Enabled: true, Logs: []string{"DFS Replication"},
			Sources: []string{"DFSR"}, IDs: []int{2104, 2213, 4012}, Count: 1, Hours: 24, Severity: model.StateCrit,
			Hint: "Replikation ist angehalten: Get-DfsrState und dfsrdiag prüfen; bei 2213 die Replikation wie in der Meldung beschrieben fortsetzen."},
		{ID: "system_errors", Name: "Alle Fehler (System)", Enabled: false, Logs: []string{"System"},
			Levels: []int{1, 2}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Meldet jeden Fehler im Systemprotokoll. Unwichtige Meldungen mit „Ignorieren“ ausblenden."},
		{ID: "app_errors", Name: "Alle Fehler (Anwendung)", Enabled: false, Logs: []string{"Application"},
			Levels: []int{1, 2}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Meldet jeden Fehler im Anwendungsprotokoll. Unwichtige Meldungen mit „Ignorieren“ ausblenden."},
	}
	return append(rules, linuxDefaults()...)
}

// Default returns the built-in rule with this id.
func Default(id string) (model.EventRule, bool) {
	for _, r := range Defaults() {
		if r.ID == id {
			return r, true
		}
	}
	return model.EventRule{}, false
}

// IsBuiltin reports whether id names a built-in rule.
func IsBuiltin(id string) bool {
	_, ok := Default(id)
	return ok
}

// Merge returns the central rules: the built-in ones, each replaced by a stored rule with the
// same ID, followed by the stored own rules.
func Merge(stored []model.EventRule) []model.EventRule {
	byID := map[string]model.EventRule{}
	for _, r := range stored {
		byID[r.ID] = r
	}
	out := Defaults()
	for i, r := range out {
		if s, ok := byID[r.ID]; ok {
			if s.Hint == "" {
				s.Hint = r.Hint
			}
			out[i] = s
			delete(byID, r.ID)
		}
	}
	for _, r := range stored {
		if _, ok := byID[r.ID]; ok {
			out = append(out, r)
		}
	}
	return model.CloneEventRules(out)
}

// Effective returns the enabled rules of a host: the central ones not switched off on it,
// then its own (an own rule with the ID of a central one replaces it).
func Effective(central []model.EventRule, off []string, own []model.EventRule) []model.EventRule {
	out := []model.EventRule{}
	seen := map[string]bool{}
	for _, r := range own {
		seen[r.ID] = true
	}
	for _, r := range central {
		if r.Enabled && !seen[r.ID] && !slices.Contains(off, r.ID) {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	for _, r := range own {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return model.CloneEventRules(out)
}

var ruleIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidID reports whether id can be a rule or exception ID.
func ValidID(id string) bool { return ruleIDRe.MatchString(id) }

func cleanStrings(list []string) []string {
	out := []string{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, s) }) {
			out = append(out, s)
		}
	}
	return out
}

func cleanInts(list []int) []int {
	out := []int{}
	for _, v := range list {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func badText(s string) bool { return strings.ContainsAny(s, "\x00\r\n\t") }

// Validate normalises a rule (trimmed lists, defaults) and returns German field errors keyed
// by prefix + JSON name ("name", "logs", …). The ID is checked by the caller.
func Validate(r *model.EventRule, prefix string) map[string]string {
	errs := map[string]string{}
	r.Name, r.Text, r.Hint = strings.TrimSpace(r.Name), strings.TrimSpace(r.Text), strings.TrimSpace(r.Hint)
	r.Logs, r.Sources = cleanStrings(r.Logs), cleanStrings(r.Sources)
	r.IDs, r.Levels = cleanInts(r.IDs), cleanInts(r.Levels)
	os := RuleOS(*r)
	if os == OSWindows {
		r.OS = ""
	} else {
		for i, l := range r.Logs {
			r.Logs[i] = strings.ToLower(l)
		}
		if len(r.Logs) == 0 || slices.Contains(r.Logs, LogJournal) {
			r.Logs = []string{LogJournal} // the journal holds the kernel messages too
		}
		if len(r.IDs) > 0 {
			errs[prefix+"ids"] = "Das Linux-Journal kennt keine Ereignis-IDs."
		}
	}
	if r.Count == 0 {
		r.Count = 1
	}
	if r.Hours == 0 {
		r.Hours = 24
	}
	if r.Severity == "" {
		r.Severity = model.StateWarn
	}
	switch {
	case r.Name == "":
		errs[prefix+"name"] = "Namen angeben."
	case len([]rune(r.Name)) > 80 || badText(r.Name):
		errs[prefix+"name"] = "Höchstens 80 Zeichen, eine Zeile."
	}
	switch {
	case len(r.Logs) == 0:
		errs[prefix+"logs"] = "Mindestens ein Protokoll angeben, z. B. System."
	case len(r.Logs) > MaxLogs:
		errs[prefix+"logs"] = fmt.Sprintf("Höchstens %d Protokolle.", MaxLogs)
	}
	for _, l := range r.Logs {
		if len(l) > 255 || badText(l) || os == OSLinux && l != LogJournal && l != LogKernel {
			errs[prefix+"logs"] = "Ungültiger Protokollname."
		}
	}
	if len(r.Sources) > MaxSources {
		errs[prefix+"sources"] = fmt.Sprintf("Höchstens %d Quellen.", MaxSources)
	}
	for _, s := range r.Sources {
		if len(s) > 255 || badText(s) {
			errs[prefix+"sources"] = "Ungültiger Quellenname."
		}
	}
	if len(r.IDs) > MaxIDs {
		errs[prefix+"ids"] = fmt.Sprintf("Höchstens %d Ereignis-IDs – sonst eine zweite Regel anlegen.", MaxIDs)
	}
	for _, id := range r.IDs {
		if id < 0 || id > 65535 {
			errs[prefix+"ids"] = "Ereignis-IDs liegen zwischen 0 und 65535."
		}
	}
	for _, l := range r.Levels {
		if _, ok := levelNames(os)[l]; !ok {
			errs[prefix+"levels"] = "Ungültige Stufe."
		}
	}
	if len([]rune(r.Text)) > MaxText || badText(r.Text) {
		errs[prefix+"text"] = fmt.Sprintf("Höchstens %d Zeichen, eine Zeile.", MaxText)
	}
	if len(r.Sources) == 0 && len(r.IDs) == 0 && len(r.Levels) == 0 && r.Text == "" {
		if os == OSLinux {
			errs[prefix+"sources"] = "Mindestens Quelle, Stufe oder Text angeben – sonst zählt jede Meldung."
		} else {
			errs[prefix+"ids"] = "Mindestens Quelle, Ereignis-ID, Stufe oder Text angeben – sonst zählt jedes Ereignis."
		}
	}
	if r.Count < 1 || r.Count > MaxCount {
		errs[prefix+"count"] = fmt.Sprintf("Anzahl zwischen 1 und %d.", MaxCount)
	}
	if r.Hours < 1 || r.Hours > MaxHours {
		errs[prefix+"hours"] = fmt.Sprintf("Zeitraum zwischen 1 und %d Stunden.", MaxHours)
	}
	if r.Severity != model.StateWarn && r.Severity != model.StateCrit {
		errs[prefix+"severity"] = "Warnung oder kritisch."
	}
	if len([]rune(r.Hint)) > 500 {
		errs[prefix+"hint"] = "Höchstens 500 Zeichen."
	}
	return errs
}

// ValidateIgnore normalises an exception and returns German field errors keyed by prefix +
// JSON name.
func ValidateIgnore(ig *model.EventIgnore, prefix string) map[string]string {
	errs := map[string]string{}
	ig.Source, ig.Text, ig.Note = strings.TrimSpace(ig.Source), strings.TrimSpace(ig.Text), strings.TrimSpace(ig.Note)
	ig.IDs = cleanInts(ig.IDs)
	if len(ig.Source) > 255 || badText(ig.Source) {
		errs[prefix+"source"] = "Ungültiger Quellenname."
	}
	if len(ig.IDs) > MaxIDs {
		errs[prefix+"ids"] = fmt.Sprintf("Höchstens %d Ereignis-IDs.", MaxIDs)
	}
	for _, id := range ig.IDs {
		if id < 0 || id > 65535 {
			errs[prefix+"ids"] = "Ereignis-IDs liegen zwischen 0 und 65535."
		}
	}
	if len([]rune(ig.Text)) > MaxText || badText(ig.Text) {
		errs[prefix+"text"] = fmt.Sprintf("Höchstens %d Zeichen, eine Zeile.", MaxText)
	}
	if len([]rune(ig.Note)) > 200 || badText(ig.Note) {
		errs[prefix+"note"] = "Höchstens 200 Zeichen, eine Zeile."
	}
	if ig.Source == "" && len(ig.IDs) == 0 && ig.Text == "" {
		errs[prefix+"source"] = "Quelle, Ereignis-ID oder Text angeben."
	}
	return errs
}

func globFold(pattern, s string) bool {
	return model.GlobMatch(strings.ToLower(pattern), strings.ToLower(s))
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// Alternatives splits a text filter at "|" ("Failed password|Invalid user").
func Alternatives(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// textMatch reports whether the message contains one of the alternatives of text.
func textMatch(msg, text string) bool {
	return text == "" || slices.ContainsFunc(Alternatives(text), func(a string) bool { return containsFold(msg, a) })
}

// sourceMatch matches a source pattern against an event: its provider or syslog identifier,
// or its systemd unit (also without ".service").
func sourceMatch(pattern string, e model.EventEntry) bool {
	return globFold(pattern, e.Source) || e.Unit != "" && (globFold(pattern, e.Unit) || globFold(pattern, strings.TrimSuffix(e.Unit, ".service")))
}

// Match reports whether an event is counted by a rule (the time window is not checked).
func Match(r model.EventRule, e model.EventEntry) bool {
	if RuleOS(r) == OSLinux {
		if !slices.Contains(r.Logs, LogJournal) && e.Log != LogKernel {
			return false // kernel messages only
		}
	} else if !slices.ContainsFunc(r.Logs, func(l string) bool { return strings.EqualFold(l, e.Log) }) {
		return false
	}
	if len(r.Sources) > 0 && !slices.ContainsFunc(r.Sources, func(s string) bool { return sourceMatch(s, e) }) {
		return false
	}
	if len(r.IDs) > 0 && !slices.Contains(r.IDs, e.ID) {
		return false
	}
	if len(r.Levels) > 0 && !slices.Contains(r.Levels, e.Level) {
		return false
	}
	return textMatch(e.Message, r.Text)
}

// Ignored returns the exception that drops the event, if any.
func Ignored(list []model.EventIgnore, e model.EventEntry) (model.EventIgnore, bool) {
	for _, ig := range list {
		if ig.Source == "" && len(ig.IDs) == 0 && ig.Text == "" {
			continue
		}
		if ig.Source != "" && !sourceMatch(ig.Source, e) {
			continue
		}
		if len(ig.IDs) > 0 && !slices.Contains(ig.IDs, e.ID) {
			continue
		}
		if !textMatch(e.Message, ig.Text) {
			continue
		}
		return ig, true
	}
	return model.EventIgnore{}, false
}

// Query is what the agent asks the system log for one rule. Windows: XPath is run against
// each log. Linux: journalctl with Args (plus --since) and, when the rule filters by text,
// --grep Grep. Both narrow the result; Match checks every filter again.
type Query struct {
	ID    string   `json:"id"`
	Logs  []string `json:"logs,omitempty"`
	XPath string   `json:"xpath,omitempty"`
	Max   int      `json:"max"`            // events per log
	Text  bool     `json:"text,omitempty"` // Windows: the message of every event is needed (text filters)
	Args  []string `json:"args,omitempty"` // Linux: journalctl arguments without --since and --grep
	Grep  string   `json:"grep,omitempty"` // Linux: case-insensitive regular expression for --grep
	Hours int      `json:"hours,omitempty"`
}

// maxTerms keeps the XPath short: the event log rejects queries with many expressions.
const maxTerms = 22

// xpathString quotes s for XPath; ok is false when s holds both quote characters.
func xpathString(s string) (string, bool) {
	switch {
	case !strings.Contains(s, "'"):
		return "'" + s + "'", true
	case !strings.Contains(s, `"`):
		return `"` + s + `"`, true
	}
	return "", false
}

// XPath builds the event log query of a rule. Sources with patterns or quotes are left to
// Match, as are all sources when the query would get too long.
func XPath(r model.EventRule) string {
	var prov, ids, levels []string
	for _, s := range r.Sources {
		q, ok := xpathString(s)
		if !ok || strings.Contains(s, "*") {
			prov = nil
			break
		}
		prov = append(prov, "@Name="+q)
	}
	for _, id := range r.IDs {
		ids = append(ids, "EventID="+strconv.Itoa(id))
	}
	for _, l := range r.Levels {
		levels = append(levels, "Level="+strconv.Itoa(l))
	}
	if len(prov)+len(ids)+len(levels) > maxTerms {
		prov = nil
	}
	var conds []string
	if len(prov) > 0 {
		conds = append(conds, "Provider["+strings.Join(prov, " or ")+"]")
	}
	for _, list := range [][]string{ids, levels} {
		if len(list) > 0 {
			conds = append(conds, "("+strings.Join(list, " or ")+")")
		}
	}
	conds = append(conds, fmt.Sprintf("TimeCreated[timediff(@SystemTime) <= %d]", int64(max(1, r.Hours))*3600*1000))
	return "*[System[" + strings.Join(conds, " and ") + "]]"
}

// Queries returns the event log queries for rules and exceptions.
func Queries(rules []model.EventRule, ignore []model.EventIgnore) []Query {
	textIgnore := slices.ContainsFunc(ignore, func(ig model.EventIgnore) bool { return ig.Text != "" })
	out := make([]Query, 0, len(rules))
	for _, r := range rules {
		// room for events dropped by exceptions
		n := min(maxQueryEvents, max(100, 3*r.Count))
		if RuleOS(r) == OSLinux {
			out = append(out, Query{ID: r.ID, Max: n, Hours: max(1, r.Hours), Args: JournalArgs(r, n), Grep: grepOf(r.Text)})
			continue
		}
		out = append(out, Query{ID: r.ID, Logs: slices.Clone(r.Logs), XPath: XPath(r), Text: r.Text != "" || textIgnore, Max: n})
	}
	return out
}

// Result is the answer of the event log to one query.
type Result struct {
	Events []model.EventEntry `json:"events"`
	// Full: a log returned as many events as asked for, so there may be more.
	Full bool   `json:"full"`
	Err  string `json:"err,omitempty"`
}

// Window describes a time window in German ("in 24 Std.", "in der letzten Stunde").
func Window(hours int) string {
	switch {
	case hours == 1:
		return "in der letzten Stunde"
	case hours%24 == 0 && hours > 24:
		return fmt.Sprintf("in %d Tagen", hours/24)
	}
	return fmt.Sprintf("in %d Std.", hours)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func intList(list []int) string {
	s := make([]string, len(list))
	for i, v := range list {
		s[i] = strconv.Itoa(v)
	}
	return strings.Join(s, ", ")
}

// Describe summarises the filters of a rule in German.
func Describe(r model.EventRule) string {
	os := RuleOS(r)
	parts := []string{strings.Join(r.Logs, ", ")}
	if os == OSLinux {
		parts[0] = "Journal"
		if !slices.Contains(r.Logs, LogJournal) {
			parts[0] = "Kernel-Meldungen"
		}
	}
	if len(r.Sources) > 0 {
		parts = append(parts, plural(len(r.Sources), "Quelle ", "Quellen ")+strings.Join(r.Sources, ", "))
	}
	if len(r.IDs) > 0 {
		parts = append(parts, "ID "+intList(r.IDs))
	}
	if len(r.Levels) > 0 {
		names := make([]string, len(r.Levels))
		for i, l := range r.Levels {
			names[i] = levelNames(os)[l]
		}
		parts = append(parts, strings.Join(names, ", "))
	}
	if alt := Alternatives(r.Text); len(alt) > 0 {
		parts = append(parts, "Text „"+strings.Join(alt, "“ oder „")+"“")
	}
	return strings.Join(parts, " · ")
}

// Trigger describes when a rule alerts ("ab 20 Ereignissen in der letzten Stunde: Warnung").
func Trigger(r model.EventRule) string {
	sev := "Warnung"
	if r.Severity == model.StateCrit {
		sev = "kritisch"
	}
	if r.Count <= 1 {
		return fmt.Sprintf("jedes Ereignis %s: %s", Window(r.Hours), sev)
	}
	return fmt.Sprintf("ab %d Ereignissen %s: %s", r.Count, Window(r.Hours), sev)
}

// CleanMessage collapses the whitespace of an event message and cuts it to n characters.
func CleanMessage(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// firstLine is the first line of a message, cut to n characters.
func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return CleanMessage(s, n)
}

// Summary is one line about an event ("14.11. 03:12 disk (7): Das Gerät … hat einen fehlerhaften Block.").
func Summary(e model.EventEntry) string {
	s := time.Unix(e.Time, 0).Local().Format("02.01. 15:04") + " " + e.Source
	if e.Log != LogJournal && e.Log != LogKernel { // Windows: with the event ID
		s += " (" + strconv.Itoa(e.ID) + ")"
	}
	if m := firstLine(e.Message, 160); m != "" {
		s += ": " + m
	}
	return s
}

// Evaluate turns the query results into one check per rule. Rules without a result (added
// after the last query) are left out until the next query.
func Evaluate(rules []model.EventRule, ignore []model.EventIgnore, results map[string]Result, now time.Time) []model.Check {
	var out []model.Check
	for _, r := range rules {
		res, ok := results[r.ID]
		if !ok {
			continue
		}
		ch := model.Check{Key: KeyPrefix + r.ID, Name: r.Name, Group: Group, State: model.StateOK,
			Facts: map[string]string{"Filter": Describe(r), "Alarm": Trigger(r)}}
		if r.Hint != "" {
			ch.Facts["Was tun"] = r.Hint
		}
		if res.Err != "" {
			ch.State, ch.Message = model.StateUnknown, "Ereignisprotokoll nicht lesbar: "+CleanMessage(res.Err, 200)
			out = append(out, ch)
			continue
		}
		since := now.Add(-time.Duration(r.Hours) * time.Hour).Unix()
		var hits []model.EventEntry
		for _, e := range res.Events {
			if e.Time < since || !Match(r, e) {
				continue
			}
			if _, drop := Ignored(ignore, e); drop {
				continue
			}
			hits = append(hits, e)
		}
		slices.SortStableFunc(hits, func(a, b model.EventEntry) int { return cmp.Compare(b.Time, a.Time) })
		n := len(hits)
		ch.Metrics = []model.Metric{{Name: MetricCount, Value: float64(n)}}
		count := fmt.Sprintf("%d %s %s", n, plural(n, "Ereignis", "Ereignisse"), Window(r.Hours))
		if res.Full && n > 0 {
			count = "Mindestens " + count
		}
		switch {
		case n == 0:
			ch.Message = "Keine Ereignisse " + Window(r.Hours)
		case n >= max(1, r.Count):
			ch.State, ch.Message = r.Severity, count+" – zuletzt "+Summary(hits[0])
		default:
			ch.Message = fmt.Sprintf("%s (Alarm ab %d) – zuletzt %s", count, r.Count, Summary(hits[0]))
		}
		for i, e := range hits {
			if i == MaxEvents {
				break
			}
			e.Message = CleanMessage(e.Message, 600)
			ch.Events = append(ch.Events, e)
		}
		out = append(out, ch)
	}
	return out
}
