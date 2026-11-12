package proxmox

import (
	"reflect"
	"testing"
)

func TestParseTaskLog(t *testing.T) {
	ti := parseTaskLog([]taskLine{
		{T: "INFO: starting new backup job: vzdump --all 1 --storage pbs --node pve2"},
		{T: "INFO: Starting Backup of VM 102 (qemu)"},
		{T: "INFO: Finished Backup of VM 102 (00:01:02)"},
		{T: "ERROR: Backup of VM 103 failed - job failed with err -5"},
		{T: "INFO: Finished Backup of VM 1020 (00:00:10)"},
		{T: "TASK ERROR: job errors"},
	})
	want := taskInfo{job: "vzdump --all 1 --storage pbs --node pve2", failed: []string{"103"}, finished: []string{"102", "1020"}}
	if !reflect.DeepEqual(ti, want) {
		t.Errorf("parseTaskLog = %+v, want %+v", ti, want)
	}
}

func TestEvalBackups(t *testing.T) {
	const jobA, jobB = "vzdump 103 104 --storage nfs", "vzdump 105 --storage pbs"
	logs := map[string]taskInfo{
		"a-fail":  {job: jobA, failed: []string{"103"}, finished: []string{"104"}},
		"a-ok":    {job: jobA, finished: []string{"103", "104"}},
		"b-ok":    {job: jobB, finished: []string{"105"}},
		"a-early": {job: jobA}, // storage error before any guest
		"a-warn":  {job: jobA, finished: []string{"103", "104"}},
		"noid-ok": {finished: []string{"103"}},
	}
	info := func(tk task) (taskInfo, bool) {
		if tk.ID != "" {
			if taskOK(tk.Status) {
				return taskInfo{finished: []string{tk.ID}}, true
			}
			return taskInfo{failed: []string{tk.ID}}, true
		}
		ti, ok := logs[tk.UPID]
		return ti, ok
	}
	job := func(upid, node, status string, start float64) task {
		return task{UPID: upid, Node: node, Type: "vzdump", Status: status, StartTime: num(start), EndTime: num(start + 60)}
	}
	single := func(id, status string, start float64) task {
		return task{UPID: "s" + id, Node: "pve2", Type: "vzdump", ID: id, Status: status, StartTime: num(start), EndTime: num(start + 60)}
	}
	names := map[string]string{"103": "nextcloud", "105": "sql01"}
	tests := []struct {
		name           string
		ts             []task
		failed, warned []string
	}{
		{"all ok", []task{job("a-ok", "pve2", "OK", 1), single("105", "OK", 2)}, nil, nil},
		{"failed guest", []task{job("a-fail", "pve2", "job errors", 1)}, []string{"103 nextcloud"}, nil},
		{"rerun of the job", []task{job("a-fail", "pve2", "job errors", 1), job("a-ok", "pve2", "OK", 2)}, nil, nil},
		{"other job does not clear", []task{job("a-fail", "pve2", "job errors", 1), job("b-ok", "pve2", "OK", 2)}, []string{"103 nextcloud"}, nil},
		{"single retry clears", []task{job("a-fail", "pve2", "job errors", 1), single("103", "OK", 2)}, nil, nil},
		{"retry before failure", []task{single("103", "OK", 1), job("a-fail", "pve2", "job errors", 2)}, []string{"103 nextcloud"}, nil},
		{"single failure, job clears", []task{single("103", "ERROR: no space", 1), job("a-ok", "pve2", "OK", 2)}, nil, nil},
		{"single failure stays", []task{single("105", "ERROR: no space", 1), job("a-ok", "pve2", "OK", 2)}, []string{"105 sql01"}, nil},
		{"job error without guests", []task{job("a-early", "pve2", "could not activate storage", 1)}, []string{"Job auf pve2 (could not activate storage)"}, nil},
		{"job error, same job ok", []task{job("a-early", "pve2", "could not activate storage", 1), job("a-ok", "pve2", "OK", 2)}, nil, nil},
		{"job error, other job ok", []task{job("a-early", "pve2", "could not activate storage", 1), job("b-ok", "pve2", "OK", 2)}, []string{"Job auf pve2 (could not activate storage)"}, nil},
		{"job error, other node", []task{job("a-early", "pve2", "could not activate storage", 1), job("a-ok", "pve1", "OK", 2)}, []string{"Job auf pve2 (could not activate storage)"}, nil},
		{"unknown log never clears", []task{job("gone", "pve2", "job errors", 1), job("a-ok", "pve2", "OK", 2)}, []string{"Job auf pve2 (job errors)"}, nil},
		{"unknown later log", []task{job("a-early", "pve2", "job errors", 1), job("gone2", "pve2", "OK", 2)}, []string{"Job auf pve2 (job errors)"}, nil},
		{"warning", []task{job("a-warn", "pve2", "WARNINGS: 1", 1)}, nil, []string{"Job auf pve2 (WARNINGS: 1)"}},
		{"warning cleared by ok run", []task{job("a-warn", "pve2", "WARNINGS: 1", 1), job("a-ok", "pve2", "OK", 2)}, nil, nil},
		{"warning not cleared by warning", []task{job("a-warn", "pve2", "WARNINGS: 1", 1), job("a-warn", "pve2", "WARNINGS: 2", 2)}, nil, []string{"Job auf pve2 (WARNINGS: 1)", "Job auf pve2 (WARNINGS: 2)"}},
		{"failure cleared by warned rerun", []task{job("a-fail", "pve2", "job errors", 1), job("a-warn", "pve2", "WARNINGS: 1", 2)}, nil, []string{"Job auf pve2 (WARNINGS: 1)"}},
		{"single warning", []task{single("103", "WARNINGS: 1", 1)}, nil, []string{"103 nextcloud"}},
		{"single warning, then failure", []task{single("103", "WARNINGS: 1", 1), single("103", "ERROR", 2)}, []string{"103 nextcloud"}, nil},
		{"single warning cleared by job", []task{single("103", "WARNINGS: 1", 1), job("noid-ok", "pve3", "OK", 2)}, nil, nil},
		{"failures merged", []task{job("a-fail", "pve2", "job errors", 1), single("103", "ERROR", 2)}, []string{"103 nextcloud"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failed, warned := evalBackups(tt.ts, info, names)
			if len(failed)+len(tt.failed) > 0 && !reflect.DeepEqual(failed, tt.failed) {
				t.Errorf("failed = %q, want %q", failed, tt.failed)
			}
			if len(warned)+len(tt.warned) > 0 && !reflect.DeepEqual(warned, tt.warned) {
				t.Errorf("warned = %q, want %q", warned, tt.warned)
			}
		})
	}
}
