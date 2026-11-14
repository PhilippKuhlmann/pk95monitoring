package agent

import (
	"testing"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
)

func TestDropSelfRestarts(t *testing.T) {
	own := model.EventEntry{Time: 1000, Log: "System", Source: "Service Control Manager", ID: 7031, Level: 2,
		Message: "The PK95 Monitoring Agent service terminated unexpectedly. It has done this 3 time(s)."}
	other := own
	other.Message = "The Windows Search service terminated unexpectedly."
	noMsg := own
	noMsg.Message, noMsg.Time = "", 1010
	later := own
	later.Time = 5000 // a real crash, not after an update
	res := map[string]eventrules.Result{"service_crash": {Events: []model.EventEntry{own, other, noMsg, later}}}
	got := dropSelfRestarts(res, []int64{998})["service_crash"].Events
	if len(got) != 2 || got[0].Message != other.Message || got[1].Time != 5000 {
		t.Fatalf("got %+v", got)
	}
	if len(res["service_crash"].Events) != 4 {
		t.Fatal("input changed")
	}
	if r := dropSelfRestarts(res, nil); len(r["service_crash"].Events) != 4 {
		t.Fatal("dropped without restarts")
	}
}

func TestParseJournal(t *testing.T) {
	out := `{"__REALTIME_TIMESTAMP":"1763100000123456","PRIORITY":"3","_TRANSPORT":"kernel","MESSAGE":"blk_update_request: I/O error, dev sda"}
{"__REALTIME_TIMESTAMP":"1763100100000000","PRIORITY":"6","SYSLOG_IDENTIFIER":"sshd-session","_SYSTEMD_UNIT":"ssh.service","MESSAGE":"Failed password for root from 203.0.113.9"}
{"__REALTIME_TIMESTAMP":"1763100200000000","PRIORITY":"4","SYSLOG_IDENTIFIER":"systemd","_SYSTEMD_UNIT":"init.scope","UNIT":"nginx.service","MESSAGE":"nginx.service: Main process exited, code=dumped"}
{"__REALTIME_TIMESTAMP":"1763100300000000","_COMM":"app","MESSAGE":[72,105,255]}
-- No entries --
`
	l := parseJournal(out)
	if len(l) != 4 {
		t.Fatalf("%+v", l)
	}
	if l[0].Log != eventrules.LogKernel || l[0].Source != "kernel" || l[0].Level != 3 || l[0].Time != 1763100000 {
		t.Errorf("kernel %+v", l[0])
	}
	if l[1].Log != eventrules.LogJournal || l[1].Source != "sshd-session" || l[1].Unit != "ssh.service" {
		t.Errorf("sshd %+v", l[1])
	}
	if l[2].Unit != "nginx.service" || l[2].Level != 4 {
		t.Errorf("systemd %+v", l[2])
	}
	if l[3].Source != "app" || l[3].Level != 6 || l[3].Message != "Hi?" {
		t.Errorf("binary %+v", l[3])
	}
}
