package eventrules

import (
	"regexp"
	"strconv"
	"strings"

	"pk95monitoring/internal/model"
)

// linuxDefaults are the built-in rules for the systemd journal of Linux servers. Kernel
// messages have no reliable priority, so these rules pick them by text.
func linuxDefaults() []model.EventRule {
	k := []string{LogKernel}
	l := func(r model.EventRule) model.EventRule {
		r.OS, r.Enabled, r.Count = OSLinux, true, max(1, r.Count)
		if r.Hours == 0 {
			r.Hours = 24
		}
		if r.Logs == nil {
			r.Logs = []string{LogJournal}
		}
		return r
	}
	return []model.EventRule{
		l(model.EventRule{ID: "oom", Name: "Speicher erschöpft (OOM-Killer)", Logs: k, Text: "Out of memory|oom-kill:", Severity: model.StateWarn,
			Hint: "Der Kernel hat einen Prozess wegen Speichermangel beendet (welcher, steht in der Meldung). Speicher erweitern oder den Verbrauch des Dienstes begrenzen."}),
		l(model.EventRule{ID: "io_error", Name: "Festplatten-E/A-Fehler", Logs: k, Text: "I/O error|critical medium error|exception Emask", Severity: model.StateCrit,
			Hint: "Lese- oder Schreibfehler eines Datenträgers: SMART-Werte und Kabel prüfen, Sicherung sicherstellen, Platte bald tauschen."}),
		l(model.EventRule{ID: "fs_error", Name: "Dateisystemfehler", Logs: k,
			Text:     "EXT4-fs error|Remounting filesystem read-only|BTRFS error|BTRFS critical|xfs_force_shutdown|Corruption of in-memory data|metadata I/O error",
			Severity: model.StateCrit,
			Hint:     "Das Dateisystem ist beschädigt oder wurde schreibgeschützt eingehängt. Ursache (Platte, Speicher) klären und fsck bzw. xfs_repair/btrfs scrub einplanen."}),
		l(model.EventRule{ID: "mce", Name: "Hardwarefehler (MCE/EDAC)", Logs: k, Text: "[Hardware Error]|Machine check events logged|EDAC MC", Severity: model.StateWarn,
			Hint: "CPU oder Arbeitsspeicher melden Fehler. Bei wiederholten Meldungen Speicher testen bzw. tauschen, BIOS und Firmware aktualisieren."}),
		l(model.EventRule{ID: "thermal", Name: "Überhitzung", Logs: k, Text: "temperature above threshold|critical temperature reached", Severity: model.StateWarn,
			Hint: "Die CPU wird wegen zu hoher Temperatur gedrosselt: Lüfter, Kühlkörper und Raumtemperatur prüfen."}),
		l(model.EventRule{ID: "smart", Name: "SMART-Warnung (smartd)", Sources: []string{"smartd"}, Levels: []int{0, 1, 2}, Severity: model.StateCrit,
			Hint: "smartd meldet einen Fehler oder einen drohenden Ausfall einer Platte: smartctl -a ansehen und die Platte tauschen."}),
		l(model.EventRule{ID: "segfault", Name: "Programmabsturz", Logs: k, Text: "segfault at|general protection fault", Severity: model.StateWarn,
			Hint: "Ein Programm ist abgestürzt (welches, steht in der Meldung). Häufen sich die Abstürze verschiedener Programme, Arbeitsspeicher testen."}),
		l(model.EventRule{ID: "unit_crash", Name: "Dienst abgestürzt", Sources: []string{"systemd"}, Text: "code=dumped|Failed with result 'core-dump'|Failed with result 'signal'",
			Severity: model.StateWarn,
			Hint:     "Ein Dienst wurde durch einen Absturz beendet: journalctl -u <dienst> zur gleichen Zeit ansehen."}),
		l(model.EventRule{ID: "ssh_failed", Name: "Fehlgeschlagene SSH-Anmeldungen", Sources: []string{"sshd", "sshd-session"},
			Text: "Failed password|Failed publickey|maximum authentication attempts", Count: 20, Hours: 1, Severity: model.StateWarn,
			Hint: "Viele Fehlversuche deuten auf Kennwort-Raten hin: Quell-IP in der Meldung prüfen, SSH nicht offen ins Internet stellen, Anmeldung per Kennwort abschalten, fail2ban einsetzen."}),
		l(model.EventRule{ID: "netfs", Name: "Netzlaufwerk nicht erreichbar", Logs: k,
			Text: "not responding, still trying|not responding, timed out|has not responded in|CIFS: VFS: Send error", Severity: model.StateWarn,
			Hint: "Ein NFS- oder SMB-Server antwortet nicht: Server, Netzwerk und Freigabe prüfen."}),
		{ID: "journal_errors", Name: "Alle Fehler (Journal)", OS: OSLinux, Enabled: false, Logs: []string{LogJournal},
			Levels: []int{0, 1, 2, 3}, Count: 1, Hours: 24, Severity: model.StateWarn,
			Hint: "Meldet jede Meldung mit Priorität „Fehler“ oder höher. Unwichtige Meldungen mit „Ignorieren“ ausblenden."},
	}
}

// unitSuffixes mark a source as a systemd unit (matched as _SYSTEMD_UNIT).
var unitSuffixes = []string{".service", ".socket", ".timer", ".mount", ".scope", ".slice", ".path", ".target"}

// JournalArgs are the journalctl arguments of a Linux rule (without --since and --grep):
// newest first, at most n, narrowed to kernel messages, the priorities up to the lowest
// wanted one and – without patterns – the sources (syslog identifier or unit).
func JournalArgs(r model.EventRule, n int) []string {
	args := []string{"-o", "json", "--no-pager", "-r", "-n", strconv.Itoa(n)}
	kernel := len(r.Logs) > 0 && !strings.EqualFold(r.Logs[0], LogJournal)
	if kernel {
		args = append(args, "-k")
	}
	if len(r.Levels) > 0 {
		args = append(args, "-p", "0.."+strconv.Itoa(max(0, maxOf(r.Levels))))
	}
	if kernel || len(r.Sources) == 0 || strings.Contains(strings.Join(r.Sources, ""), "*") {
		return args // matches with "+" would undo -k; patterns are checked by Match
	}
	for i, s := range r.Sources {
		if i > 0 {
			args = append(args, "+")
		}
		field := "SYSLOG_IDENTIFIER="
		for _, suf := range unitSuffixes {
			if strings.HasSuffix(s, suf) {
				field = "_SYSTEMD_UNIT="
			}
		}
		args = append(args, field+s)
	}
	return args
}

func maxOf(list []int) int {
	m := list[0]
	for _, v := range list[1:] {
		m = max(m, v)
	}
	return m
}

// grepOf turns a text filter into a regular expression for journalctl --grep ("" = none).
func grepOf(text string) string {
	alt := Alternatives(text)
	for i, a := range alt {
		alt[i] = regexp.QuoteMeta(a)
	}
	return strings.Join(alt, "|")
}
