package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"pk95monitoring/internal/model"
)

// The device page acknowledges open alerts per check: an open alert older than the last 20
// (a Proxmox host with one alert per guest backup) must still come with the device.
func TestGetDeviceOpenAlerts(t *testing.T) {
	c := newTestCore(t, t.TempDir())
	d, _, err := c.SaveDevice(model.Device{Name: "pve01", Type: model.TypeNet, Host: "pve01.example.lan", Enabled: false}) // not polled: no alerts of its own
	if err != nil {
		t.Fatal(err)
	}
	old, err := c.St.OpenAlert(model.Alert{DeviceID: d.ID, DeviceName: d.Name, CheckKey: "pve:storage:pve01:pbs01", Severity: model.StateWarn, Started: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// 25 newer alerts, every second one closed: 12 open, 13 closed
	for i := range 25 {
		id, err := c.St.OpenAlert(model.Alert{DeviceID: d.ID, DeviceName: d.Name, CheckKey: "pve:bak:" + strconv.Itoa(100+i), Severity: model.StateWarn, Started: int64(2000 + i)})
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := c.St.CloseAlert(id, int64(3000+i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := &Server{C: c}
	req := httptest.NewRequest(http.MethodGet, "/api/devices/"+d.ID, nil)
	req.SetPathValue("id", d.ID)
	rec := httptest.NewRecorder()
	s.getDevice(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Alerts []model.Alert `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	found, open := false, 0
	for i, a := range out.Alerts {
		if a.ID == old {
			found = true
		}
		if a.Ended == 0 {
			open++
		}
		if i > 0 && a.ID >= out.Alerts[i-1].ID {
			t.Fatalf("not newest first: %d after %d", a.ID, out.Alerts[i-1].ID)
		}
	}
	// the last 20 plus the three open alerts before them (the storage alert among them)
	if !found || open != 13 || len(out.Alerts) != 23 {
		t.Fatalf("storage alert found %v, open %d, total %d", found, open, len(out.Alerts))
	}
}

// POST /api/alerts/{id}/ack takes an optional period and note.
func TestAckAlertPeriodAndNote(t *testing.T) {
	c := newTestCore(t, t.TempDir())
	d, _, err := c.SaveDevice(model.Device{Name: "pve01", Type: model.TypeNet, Host: "pve01.example.lan", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.St.OpenAlert(model.Alert{DeviceID: d.ID, CheckKey: "pve:storage:pve01:pbs01", Severity: model.StateWarn})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{C: c}
	ack := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/alerts/x/ack", strings.NewReader(body))
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		rec := httptest.NewRecorder()
		s.ackAlert(rec, req)
		return rec.Code
	}
	now := c.Now().Unix()
	for _, bad := range []string{
		`{"until": 1000}`,
		`{"until": ` + strconv.FormatInt(now+400*86400, 10) + `}`,
		`{"note": "` + strings.Repeat("x", 501) + `"}`,
		`{"until": "morgen"}`,
	} {
		if code := ack(bad); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
			t.Errorf("%.40s: %d", bad, code)
		}
	}
	until := now + 7*86400
	if code := ack(`{"until": ` + strconv.FormatInt(until, 10) + `, "note": "Platte bestellt"}`); code != http.StatusOK {
		t.Fatalf("ack: %d", code)
	}
	list, _ := c.St.ActiveAlerts()
	if list[0].AckUntil != until || list[0].AckNote != "Platte bestellt" || list[0].AckedBy == "" && list[0].AckedAt == 0 {
		t.Fatalf("stored %+v", list[0])
	}
	// without a body: until it gets worse
	if code := ack(""); code != http.StatusOK {
		t.Fatalf("ack without body: %d", code)
	}
	if list, _ = c.St.ActiveAlerts(); list[0].AckUntil != 0 || list[0].AckNote != "" {
		t.Fatalf("stored %+v", list[0])
	}
}
