package store

import (
	"context"
	"errors"
	"testing"

	"pk95monitoring/internal/model"
)

func TestAlertLifecycle(t *testing.T) {
	s := newStore(t)
	a := model.Alert{DeviceID: "d1", DeviceName: "srv01", CheckKey: "disk:/", CheckName: "Festplatte /",
		Severity: model.StateWarn, Message: "Festplatte / 91 % ≥ 85 %", Started: 1000}
	id, err := s.OpenAlert(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAlert(id, model.StateCrit, "Festplatte / 97 % ≥ 95 %"); err != nil {
		t.Fatal(err)
	}
	if err := s.AckAlert(id, "admin", 1100); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveAlerts()
	if err != nil || len(active) != 1 {
		t.Fatalf("ActiveAlerts = %v %v", active, err)
	}
	want := a
	want.ID, want.Severity, want.Message, want.AckedBy, want.AckedAt = id, model.StateCrit, "Festplatte / 97 % ≥ 95 %", "admin", 1100
	if active[0] != want {
		t.Fatalf("alert = %+v\nwant  %+v", active[0], want)
	}
	if err := s.CloseAlert(id, 1200); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseAlert(id, 1300); err != nil { // already ended: unchanged
		t.Fatal(err)
	}
	if active, _ := s.ActiveAlerts(); len(active) != 0 {
		t.Fatalf("still active: %v", active)
	}
	all, _ := s.Alerts(context.Background(), AlertQuery{})
	if len(all) != 1 || all[0].Ended != 1200 {
		t.Fatalf("closed alert = %+v", all)
	}
	for _, err := range []error{s.UpdateAlert(999, "crit", ""), s.AckAlert(999, "x", 1)} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown alert: %v", err)
		}
	}

	// PruneAlerts removes only ended alerts older than the limit.
	open, _ := s.OpenAlert(model.Alert{DeviceID: "d1", CheckKey: "cpu", Severity: model.StateWarn, Started: 10})
	if err := s.PruneAlerts(5000); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Alerts(context.Background(), AlertQuery{})
	if len(all) != 1 || all[0].ID != open {
		t.Fatalf("after prune: %+v", all)
	}
}

func TestAlertsQuery(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var ids []int64
	for i := range 10 {
		dev := "d1"
		if i%2 == 1 {
			dev = "d2"
		}
		id, err := s.OpenAlert(model.Alert{DeviceID: dev, CheckKey: "cpu", Severity: model.StateWarn, Started: int64(100 + i)})
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			if err := s.CloseAlert(id, int64(200+i)); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, id)
	}
	tests := []struct {
		name string
		q    AlertQuery
		want []int64
	}{
		{"all newest first", AlertQuery{Limit: 3}, []int64{ids[9], ids[8], ids[7]}},
		{"page 2", AlertQuery{Limit: 3, Before: ids[7]}, []int64{ids[6], ids[5], ids[4]}},
		{"last page", AlertQuery{Limit: 3, Before: ids[1]}, []int64{ids[0]}},
		{"device", AlertQuery{DeviceID: "d2", Limit: 2}, []int64{ids[9], ids[7]}},
		{"active only", AlertQuery{ActiveOnly: true, Before: ids[6]}, []int64{ids[5], ids[4]}},
		{"active device", AlertQuery{ActiveOnly: true, DeviceID: "d1"}, []int64{ids[8], ids[6], ids[4]}},
		{"default limit", AlertQuery{}, []int64{ids[9], ids[8], ids[7], ids[6], ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Alerts(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			var gotIDs []int64
			for _, a := range got {
				gotIDs = append(gotIDs, a.ID)
			}
			if len(gotIDs) != len(tt.want) {
				t.Fatalf("ids = %v, want %v", gotIDs, tt.want)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.want[i] {
					t.Fatalf("ids = %v, want %v", gotIDs, tt.want)
				}
			}
		})
	}
}

func TestStatuses(t *testing.T) {
	s := newStore(t)
	st := model.DeviceStatus{
		DeviceID: "d1", State: model.StateWarn, Message: "Festplatte / 91 %", Since: 10, LastPoll: 20, LastOK: 20,
		Checks: []model.CheckStatus{{
			Check:  model.Check{Key: "disk:/", Name: "Festplatte /", State: model.StateWarn, Metrics: []model.Metric{{Name: "disk.usage", Instance: "/", Value: 91, Unit: "%"}}},
			Native: model.StateOK, Since: 10, Reason: "Festplatte / 91 % ≥ 85 %",
		}},
	}
	if err := s.SaveStatus(st); err != nil {
		t.Fatal(err)
	}
	st.LastPoll = 30
	if err := s.SaveStatus(st); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveStatus(model.DeviceStatus{DeviceID: "d2", State: model.StateOK}); err != nil {
		t.Fatal(err)
	}
	all, err := s.LoadStatuses()
	if err != nil || len(all) != 2 {
		t.Fatalf("LoadStatuses = %v %v", all, err)
	}
	got := all["d1"]
	if got.LastPoll != 30 || len(got.Checks) != 1 || got.Checks[0].Reason != st.Checks[0].Reason || got.Checks[0].Metrics[0] != st.Checks[0].Metrics[0] {
		t.Fatalf("status = %+v", got)
	}
	if err := s.DeleteStatus("d1"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.LoadStatuses(); len(all) != 1 {
		t.Fatalf("after delete: %v", all)
	}
}
