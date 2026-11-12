package fakepve

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, s *Server, path, auth string) (int, map[string]any) {
	t.Helper()
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test server
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/api2/json"+path, nil)
	req.Header.Set("Authorization", auth)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v (%s)", path, err, raw)
	}
	return resp.StatusCode, out
}

func TestServer(t *testing.T) {
	s := New()
	defer s.Close()
	auth := "PVEAPIToken=" + s.TokenID + "=" + s.Secret

	if code, _ := get(t, s, "/version", "PVEAPIToken="+s.TokenID+"=wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	tests := []struct {
		path string
		code int
	}{
		{"/version", 200},
		{"/cluster/status", 200},
		{"/cluster/resources", 200},
		{"/cluster/tasks", 200},
		{"/nodes/pve2/tasks?typefilter=vzdump", 200},
		{"/nodes/pve1/status", 200},
		{"/nodes/pve1/disks/zfs", 200},
		{"/nodes/pve1/disks/list", 200},
		{"/nodes/pve1/apt/update", 200},
		{"/nodes/pve2/qemu/107/config", 200},
		{"/nodes/pve1/qemu/107/config", 500},
		{"/nodes/pve9/status", 500},
		{"/nodes/pve1/nothing", 501},
	}
	for _, tt := range tests {
		if code, _ := get(t, s, tt.path, auth); code != tt.code {
			t.Errorf("%s: %d, want %d", tt.path, code, tt.code)
		}
	}
	_, cfg := get(t, s, "/nodes/pve2/qemu/107/config", auth)
	if cfg["data"].(map[string]any)["onboot"] != 1.0 {
		t.Errorf("107 config %v", cfg)
	}

	_, tl := get(t, s, "/nodes/pve2/tasks?typefilter=vzdump&since="+strconv.FormatInt(s.Now().Add(-24*time.Hour).Unix(), 10), auth)
	for _, x := range tl["data"].([]any) {
		task := x.(map[string]any)
		if task["node"] != "pve2" || task["type"] != "vzdump" || task["id"] == "105" {
			t.Errorf("node task filter: %v", task)
		}
	}
	if n := len(tl["data"].([]any)); n != 2 { // the nightly job and the single backup of CT 201
		t.Errorf("pve2 vzdump tasks: %d", n)
	}

	s.SetNodeOnline("pve3", false)
	if code, _ := get(t, s, "/nodes/pve3/status", auth); code != 595 {
		t.Errorf("offline node: %d", code)
	}
	_, res := get(t, s, "/cluster/resources?type=vm", auth)
	for _, r := range res["data"].([]any) {
		g := r.(map[string]any)
		if g["node"] == "pve3" && g["status"] != "unknown" {
			t.Errorf("guest on offline node: %v", g)
		}
		if g["type"] != "qemu" && g["type"] != "lxc" {
			t.Errorf("type filter: %v", g)
		}
	}

	s.SetForbidden("/apt/", true)
	if code, _ := get(t, s, "/nodes/pve1/apt/update", auth); code != http.StatusForbidden {
		t.Errorf("forbidden: %d", code)
	}
	if len(strings.Split(s.Fingerprint(), ":")) != 32 || s.Port() == 0 || s.Host() == "" {
		t.Errorf("fingerprint/host/port: %s %s %d", s.Fingerprint(), s.Host(), s.Port())
	}
}

func TestCountersMonotonic(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	prev := 0.0
	for i := 1; i < 2000; i++ {
		v := counter(1e6, start, start.Add(time.Duration(i)*7*time.Second), 47*time.Minute, 1.3)
		if v < prev {
			t.Fatalf("counter went down at %d: %v < %v", i, v, prev)
		}
		prev = v
	}
}

func TestBackupTasks(t *testing.T) {
	s := New()
	defer s.Close()
	now := s.Now()
	failed := 0
	for _, task := range s.tasks(now) {
		if task.typ == "vzdump" && task.status == "job errors" {
			failed++
			if !strings.Contains(strings.Join(task.log, "\n"), "ERROR: Backup of VM 103 failed") {
				t.Errorf("log %v", task.log)
			}
		}
		if task.end.After(now) {
			t.Errorf("task ends in the future: %+v", task)
		}
	}
	if failed != 1 {
		t.Errorf("failed jobs %d", failed)
	}
	s.SetBackupFailed(false)
	for _, task := range s.tasks(now) {
		if task.status == "job errors" {
			t.Error("job still failing")
		}
	}
}
