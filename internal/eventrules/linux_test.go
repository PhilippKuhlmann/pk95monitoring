package eventrules

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestLinuxDefaults(t *testing.T) {
	ssh, ok := Default("ssh_failed")
	if !ok || ssh.OS != OSLinux || ssh.Hours != 1 || ssh.Count != 20 {
		t.Fatalf("ssh: %+v", ssh)
	}
	if all, _ := Default("journal_errors"); all.Enabled {
		t.Fatal("catch-all on")
	}
	if n := len(ForOS(Defaults(), OSLinux)); n != 11 {
		t.Fatalf("linux rules: %d", n)
	}
	if n := len(ForOS(Defaults(), OSWindows)); n != 12 {
		t.Fatalf("windows rules: %d", n)
	}
}

func TestLinuxValidate(t *testing.T) {
	r := model.EventRule{OS: OSLinux, Name: "x", Logs: []string{"Kernel", "journal"}, IDs: []int{7}, Levels: []int{8}}
	errs := Validate(&r, "")
	if errs["ids"] == "" || errs["levels"] == "" || !slices.Equal(r.Logs, []string{LogJournal}) {
		t.Fatalf("%v %+v", errs, r)
	}
	if errs := Validate(&model.EventRule{OS: OSLinux, Name: "x", Logs: []string{"System"}, Text: "a"}, ""); errs["logs"] == "" {
		t.Fatal("windows log accepted")
	}
	if errs := Validate(&model.EventRule{OS: OSLinux, Name: "x"}, ""); errs["sources"] == "" {
		t.Fatal("rule without filter accepted")
	}
	ok := model.EventRule{OS: OSLinux, Name: "Kernel", Logs: []string{"kernel"}, Text: "a|b"}
	if errs := Validate(&ok, ""); len(errs) > 0 || ok.Logs[0] != LogKernel {
		t.Fatalf("%v %+v", errs, ok)
	}
}

func TestLinuxMatch(t *testing.T) {
	io, _ := Default("io_error")
	kern := model.EventEntry{Log: LogKernel, Source: "kernel", Level: 3, Message: "blk_update_request: I/O error, dev sda, sector 123"}
	if !Match(io, kern) {
		t.Fatal("kernel I/O error")
	}
	if Match(io, model.EventEntry{Log: LogJournal, Source: "app", Message: "I/O error in app"}) {
		t.Fatal("kernel-only rule matched a service message")
	}
	crash, _ := Default("unit_crash")
	if !Match(crash, model.EventEntry{Log: LogJournal, Source: "systemd", Unit: "init.scope", Level: 4, Message: "nginx.service: Main process exited, code=dumped, status=11/SEGV"}) {
		t.Fatal("core dump")
	}
	unit := model.EventRule{OS: OSLinux, Logs: []string{LogJournal}, Sources: []string{"nginx"}, Levels: []int{0, 1, 2, 3}}
	e := model.EventEntry{Log: LogJournal, Source: "nginx", Unit: "nginx.service", Level: 3, Message: "x"}
	if !Match(unit, e) || !Match(model.EventRule{OS: OSLinux, Logs: []string{LogJournal}, Sources: []string{"nginx.service"}}, e) || Match(unit, model.EventEntry{Log: LogJournal, Source: "nginx", Level: 6}) {
		t.Fatal("unit / level")
	}
	ig := []model.EventIgnore{{Source: "nginx", Text: "harmlos|egal"}}
	if _, ok := Ignored(ig, model.EventEntry{Source: "x", Unit: "nginx.service", Message: "ist EGAL"}); !ok {
		t.Fatal("ignore by unit and alternative text")
	}
	if s := Summary(model.EventEntry{Time: time.Now().Unix(), Log: LogKernel, Source: "kernel", Message: "Out of memory: Killed process 42 (java)"}); strings.Contains(s, "(0)") || !strings.Contains(s, "kernel: Out of memory") {
		t.Fatal(s)
	}
	oom, _ := Default("oom")
	if d := Describe(oom); d != "Kernel-Meldungen · Text „Out of memory“ oder „oom-kill:“" {
		t.Fatal(d)
	}
}

func TestJournalQuery(t *testing.T) {
	ssh, _ := Default("ssh_failed")
	q := Queries([]model.EventRule{ssh}, nil)[0]
	want := []string{"-o", "json", "--no-pager", "-r", "-n", "100", "SYSLOG_IDENTIFIER=sshd", "+", "SYSLOG_IDENTIFIER=sshd-session"}
	if !slices.Equal(q.Args, want) || q.Hours != 1 || q.Grep != "Failed password|Failed publickey|maximum authentication attempts" || q.XPath != "" {
		t.Fatalf("ssh query: %+v", q)
	}
	smart, _ := Default("smart")
	if a := JournalArgs(smart, 100); !slices.Contains(a, "0..2") || !slices.Contains(a, "SYSLOG_IDENTIFIER=smartd") {
		t.Fatal(a)
	}
	fs, _ := Default("fs_error")
	if a := JournalArgs(fs, 100); !slices.Contains(a, "-k") || slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, "=") }) {
		t.Fatal(a)
	}
	if a := JournalArgs(model.EventRule{OS: OSLinux, Logs: []string{LogJournal}, Sources: []string{"nginx.service", "php*"}}, 50); slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, "=") }) {
		t.Fatal("pattern used as match:", a)
	}
	if a := JournalArgs(model.EventRule{OS: OSLinux, Logs: []string{LogJournal}, Sources: []string{"nginx.service"}}, 50); a[len(a)-1] != "_SYSTEMD_UNIT=nginx.service" {
		t.Fatal(a)
	}
	if g := grepOf("a.b|[x]"); g != `a\.b|\[x\]` {
		t.Fatal(g)
	}
}
