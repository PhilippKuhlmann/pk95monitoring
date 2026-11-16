package proxmox

import (
	"testing"

	"pk95monitoring/internal/model"
)

// The backup jobs of /cluster/backup come with the check "pve:backup" and their latest runs:
// tasks of several nodes started together are one run, task logs are read newest first.
func TestBackupJobs(t *testing.T) {
	_, c, d := setup(t)
	jobs := collect(t, c, d)["pve:backup"].Jobs
	if len(jobs) != 3 {
		t.Fatalf("jobs %+v", jobs)
	}
	// the first poll reads only some logs, but tonight's run is among them
	if r := jobs[1].Runs; len(r) == 0 || r[len(r)-1].State != model.StateCrit {
		t.Fatalf("first poll: nightly runs %+v", r)
	}
	for range 3 {
		jobs = collect(t, c, d)["pve:backup"].Jobs
	}

	test, nightly, weekly := jobs[0], jobs[1], jobs[2]
	if test.ID != "backup-test" || test.Enabled || test.Name != "106 testvm" || len(test.Runs) != 0 {
		t.Errorf("disabled job %+v", test)
	}
	if nightly.ID != "backup-nightly" || nightly.Name != "Nachtsicherung" || nightly.Selection != "Alle außer 106" ||
		nightly.Schedule != "01:00" || nightly.Target != "backup" || !nightly.Enabled || nightly.Next <= c.now().Unix() {
		t.Errorf("nightly job %+v", nightly)
	}
	// 14 nights in the archive, the newest 10 reported (oldest first): night 9 failed on
	// pve3, night 4 had a warning on pve1, tonight pve2 failed
	r := nightly.Runs
	if len(r) != model.MaxJobRuns {
		t.Fatalf("nightly runs %d: %+v", len(r), r)
	}
	for i, x := range r {
		want, msg := model.StateOK, "Erfolgreich"
		switch i {
		case 0:
			want, msg = model.StateCrit, "Fehlgeschlagen: 202 grafana"
		case 5:
			want, msg = model.StateWarn, "Mit Warnungen"
		case 9:
			want, msg = model.StateCrit, "Fehlgeschlagen: 103 nextcloud"
		}
		if x.State != want || x.Message != msg {
			t.Errorf("nightly run %d: %+v, want %s %q", i, x, want, msg)
		}
		if i > 0 {
			if d := x.Start - r[i-1].Start; d < 23*3600 || d > 25*3600 { // a day, DST included
				t.Errorf("run %d starts %d s after the one before", i, d)
			}
		}
		// the tasks of pve1, pve2 and pve3 (12 minutes apart) are one run
		if x.End-x.Start < 24*60 {
			t.Errorf("run %d lasts %d s: not all nodes", i, x.End-x.Start)
		}
	}
	if weekly.ID != "backup-weekly" || weekly.Selection != "101 fileserver, 105 sql01" || weekly.Schedule != "sun 03:00" {
		t.Errorf("weekly job %+v", weekly)
	}
	if len(weekly.Runs) != 5 || weekly.Runs[2].State != model.StateWarn || weekly.Runs[4].State != model.StateOK {
		t.Errorf("weekly runs %+v", weekly.Runs)
	}
}

func TestMatchJob(t *testing.T) {
	jobs := []backupJobConf{
		{ID: "all", Storage: "pbs", All: 1, Exclude: "106"},
		{ID: "some", Storage: "pbs", VMID: "100,101,105"},
		{ID: "pool", Storage: "nas", Pool: "prod"},
	}
	for _, x := range []struct{ cmd, want string }{
		{"vzdump --all 1 --exclude 106 --mode snapshot --storage pbs --node pve1", "all"},
		{"vzdump --all 1 --mode snapshot --storage pbs", ""},     // other exclude list
		{"vzdump 101 105 --mode snapshot --storage pbs", "some"}, // the guests of one node
		{"vzdump 101 102 --storage pbs", ""},
		{"vzdump 105 --storage pbs --mode snapshot --remove 0 --notes-template '{{guestname}}'", ""}, // "Backup now"
		{"vzdump --pool prod --storage nas", "pool"},
		{"vzdump 100 --storage=local --job-id some", "some"}, // the job ID decides
		{"vzdump 100 --storage pbs --job-id gone", ""},
		{"", ""},
	} {
		if got := matchJob(parseVzdump(x.cmd), jobs); got != x.want {
			t.Errorf("%q: %q, want %q", x.cmd, got, x.want)
		}
	}
}

// A failed single-guest task names its guest, a failure without a known guest its status.
func TestJobRuns(t *testing.T) {
	names := map[string]string{"105": "sql01"}
	ts := []task{
		{UPID: "b", Node: "pve1", Status: "job errors", StartTime: 1000 + 86400, EndTime: 1000 + 86400 + 60},
		{UPID: "a", Node: "pve3", ID: "105", Status: "unable to connect", StartTime: 1000, EndTime: 1060},
	}
	runs := jobRuns(ts, map[string]jobLog{}, names)
	if len(runs) != 2 || runs[0].Message != "Fehlgeschlagen: 105 sql01" || runs[0].End != 1060 || runs[1].Message != "Fehlgeschlagen (job errors)" {
		t.Errorf("runs %+v", runs)
	}
}
