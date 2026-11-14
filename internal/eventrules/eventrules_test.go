package eventrules

import (
	"strings"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

func TestDefaultsValid(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Defaults() {
		if !ValidID(r.ID) || seen[r.ID] {
			t.Errorf("bad or duplicate id %q", r.ID)
		}
		seen[r.ID] = true
		if errs := Validate(&r, ""); len(errs) > 0 {
			t.Errorf("%s: %v", r.ID, errs)
		}
	}
	if def, _ := Default("system_errors"); def.Enabled {
		t.Error("catch-all rule must be off by default")
	}
}

func TestMergeEffective(t *testing.T) {
	disk, _ := Default("disk")
	disk.Enabled, disk.Hint = false, ""
	own := model.EventRule{ID: "abc123", Name: "Eigene", Enabled: true, Logs: []string{"Application"}, IDs: []int{1000}}
	central := Merge([]model.EventRule{disk, own})
	if central[0].ID != "disk" || central[0].Enabled || central[0].Hint == "" {
		t.Fatalf("override: %+v", central[0])
	}
	if last := central[len(central)-1]; last.ID != "abc123" || len(central) != len(Defaults())+1 {
		t.Fatalf("custom rule: %d %+v", len(central), last)
	}
	host := model.EventRule{ID: "host01", Name: "Host", Enabled: true, Logs: []string{"System"}, IDs: []int{1}}
	eff := Effective(central, []string{"bugcheck"}, []model.EventRule{host})
	ids := map[string]bool{}
	for _, r := range eff {
		ids[r.ID] = true
	}
	if ids["disk"] || ids["bugcheck"] || ids["system_errors"] || !ids["abc123"] || !ids["host01"] || !ids["lockout"] {
		t.Fatalf("effective: %v", ids)
	}
}

func TestValidate(t *testing.T) {
	r := model.EventRule{Name: " X ", Logs: []string{" System ", "system", ""}, IDs: []int{7, 7, 3}}
	if errs := Validate(&r, "r."); len(errs) > 0 {
		t.Fatal(errs)
	}
	if r.Name != "X" || len(r.Logs) != 1 || r.IDs[0] != 3 || len(r.IDs) != 2 || r.Count != 1 || r.Hours != 24 || r.Severity != model.StateWarn {
		t.Fatalf("normalised: %+v", r)
	}
	bad := model.EventRule{Logs: []string{"System"}, Count: 9999, Hours: -1, Severity: "ok", Levels: []int{9}}
	errs := Validate(&bad, "")
	for _, k := range []string{"name", "count", "hours", "severity", "levels"} {
		if errs[k] == "" {
			t.Errorf("no error for %s: %v", k, errs)
		}
	}
	if errs := Validate(&model.EventRule{Name: "x", Logs: []string{"System"}}, ""); errs["ids"] == "" {
		t.Error("rule without filter accepted")
	}
	ig := model.EventIgnore{}
	if ValidateIgnore(&ig, "")["source"] == "" {
		t.Error("empty exception accepted")
	}
}

func TestMatchIgnore(t *testing.T) {
	r := model.EventRule{Logs: []string{"System"}, Sources: []string{"Service*"}, IDs: []int{7031}, Levels: []int{2}, Text: "spooler"}
	e := model.EventEntry{Log: "system", Source: "Service Control Manager", ID: 7031, Level: 2, Message: "Der Dienst Druckwarteschlange (Spooler) wurde unerwartet beendet."}
	if !Match(r, e) {
		t.Fatal("no match")
	}
	for _, mod := range []func(*model.EventEntry){
		func(x *model.EventEntry) { x.Log = "Application" },
		func(x *model.EventEntry) { x.Source = "disk" },
		func(x *model.EventEntry) { x.ID = 7034 },
		func(x *model.EventEntry) { x.Level = 3 },
		func(x *model.EventEntry) { x.Message = "anderer Dienst" },
	} {
		x := e
		mod(&x)
		if Match(r, x) {
			t.Errorf("matched %+v", x)
		}
	}
	list := []model.EventIgnore{{ID: "a", Source: "disk"}, {ID: "b", Source: "service control manager", IDs: []int{7031}, Text: "SPOOLER"}}
	if ig, ok := Ignored(list, e); !ok || ig.ID != "b" {
		t.Fatalf("ignored: %v %+v", ok, ig)
	}
	e.ID = 7034
	if _, ok := Ignored(list, e); ok {
		t.Fatal("wrong id ignored")
	}
}

func TestQueries(t *testing.T) {
	rules := []model.EventRule{
		{ID: "a", Logs: []string{"System"}, Sources: []string{"disk", "Ntfs"}, IDs: []int{7, 11}, Levels: []int{1, 2}, Count: 1, Hours: 24},
		{ID: "b", Logs: []string{"Security"}, Sources: []string{"Microsoft-*"}, Levels: []int{0}, Count: 400, Hours: 1},
	}
	q := Queries(rules, []model.EventIgnore{{Text: "x"}})
	want := "*[System[Provider[@Name='disk' or @Name='Ntfs'] and (EventID=7 or EventID=11) and (Level=1 or Level=2) and TimeCreated[timediff(@SystemTime) <= 86400000]]]"
	if len(q) != 2 || q[0].XPath != want || q[0].Max != 100 || !q[0].Text || q[0].Logs[0] != "System" {
		t.Fatalf("query a: %+v", q[0])
	}
	if q[1].XPath != "*[System[(Level=0) and TimeCreated[timediff(@SystemTime) <= 3600000]]]" || q[1].Max != 500 {
		t.Fatalf("query b: %+v", q[1])
	}
	if x := XPath(model.EventRule{Sources: []string{`it's`}, Hours: 1}); !strings.Contains(x, `@Name="it's"`) {
		t.Fatal(x)
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Now()
	ago := func(h float64) int64 { return now.Add(-time.Duration(h * float64(time.Hour))).Unix() }
	disk, _ := Default("disk")
	logons, _ := Default("failed_logons")
	crash, _ := Default("service_crash")
	missing := model.EventRule{ID: "new", Name: "Neu", Logs: []string{"System"}, IDs: []int{1}}
	results := map[string]Result{
		"disk": {Events: []model.EventEntry{
			{Time: ago(30), Log: "System", Source: "disk", ID: 7, Level: 2, Message: "zu alt"},
			{Time: ago(2), Log: "System", Source: "disk", ID: 7, Level: 2, Message: "Das Gerät \\Device\\Harddisk1\\DR1 hat einen fehlerhaften Block.\r\nZweite Zeile"},
		}},
		"failed_logons": {Events: []model.EventEntry{
			{Time: ago(0.2), Log: "Security", Source: "Microsoft-Windows-Security-Auditing", ID: 4625, Message: "Fehler beim Anmelden eines Kontos."},
			{Time: ago(0.5), Log: "Security", Source: "Microsoft-Windows-Security-Auditing", ID: 4625, Message: "Fehler beim Anmelden eines Kontos."},
		}},
		"service_crash": {Events: []model.EventEntry{
			{Time: ago(1), Log: "System", Source: "Service Control Manager", ID: 7031, Level: 2, Message: "Der Dienst Windows Search wurde unerwartet beendet."},
		}},
	}
	ignore := []model.EventIgnore{{ID: "i1", Source: "Service Control Manager", Text: "Windows Search"}}
	checks := Evaluate([]model.EventRule{disk, logons, crash, missing}, ignore, results, now)
	// the rule without a result yet is unknown (keeps an open alert)
	if len(checks) != 4 || checks[3].Key != "ev:new" || checks[3].State != model.StateUnknown {
		t.Fatalf("checks: %+v", checks)
	}
	if pending := Evaluate([]model.EventRule{disk}, nil, nil, now); len(pending) != 1 || pending[0].State != model.StateUnknown {
		t.Fatalf("before the first query: %+v", pending)
	}
	d := checks[0]
	if d.Key != "ev:disk" || d.Group != Group || d.State != model.StateCrit || len(d.Events) != 1 || d.Metrics[0].Value != 1 ||
		!strings.HasPrefix(d.Message, "1 Ereignis in 24 Std. – zuletzt ") || strings.Contains(d.Message, "Zweite") || d.Facts["Was tun"] == "" {
		t.Fatalf("disk: %+v", d)
	}
	if l := checks[1]; l.State != model.StateOK || !strings.Contains(l.Message, "(Alarm ab 20)") || l.Metrics[0].Value != 2 || l.Events[0].Time != ago(0.2) {
		t.Fatalf("logons: %+v", l)
	}
	if c := checks[2]; c.State != model.StateOK || c.Message != "Keine Ereignisse in 24 Std." || c.Events != nil {
		t.Fatalf("ignored crash: %+v", c)
	}
	res := Evaluate([]model.EventRule{disk}, nil, map[string]Result{"disk": {Err: "Zugriff verweigert"}}, now)
	if res[0].State != model.StateUnknown {
		t.Fatalf("error: %+v", res[0])
	}
	full := Evaluate([]model.EventRule{crash}, nil, map[string]Result{"service_crash": {Full: true, Events: results["service_crash"].Events}}, now)
	if !strings.HasPrefix(full[0].Message, "Mindestens 1 Ereignis") || full[0].State != model.StateWarn {
		t.Fatalf("full: %+v", full[0])
	}
}

func TestTexts(t *testing.T) {
	if Window(1) != "in der letzten Stunde" || Window(168) != "in 7 Tagen" || Window(24) != "in 24 Std." {
		t.Fatal("window")
	}
	r, _ := Default("failed_logons")
	if Trigger(r) != "ab 20 Ereignissen in der letzten Stunde: Warnung" {
		t.Fatal(Trigger(r))
	}
	if d := Describe(model.EventRule{Logs: []string{"System"}, Sources: []string{"disk"}, IDs: []int{7, 11}, Levels: []int{2}}); d != "System · Quelle disk · ID 7, 11 · Fehler" {
		t.Fatal(d)
	}
}
