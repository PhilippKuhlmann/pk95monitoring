package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"pk95monitoring/internal/model"
)

// ---- alerts ----

const alertCols = "id, device_id, device_name, check_key, check_name, severity, message, started, ended, acked_by, acked_at"

// OpenAlert stores a new active alert (Started 0 = now) and returns its id.
func (s *Store) OpenAlert(a model.Alert) (int64, error) {
	if a.Started == 0 {
		a.Started = time.Now().Unix()
	}
	res, err := s.db.Exec(`INSERT INTO alerts (device_id, device_name, check_key, check_name, severity, message, started, ended)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`, a.DeviceID, a.DeviceName, a.CheckKey, a.CheckName, a.Severity, a.Message, a.Started)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAlert changes severity and message of an alert (e.g. escalation warn → crit).
func (s *Store) UpdateAlert(id int64, severity, message string) error {
	return affected(s.db.Exec("UPDATE alerts SET severity = ?, message = ? WHERE id = ?", severity, message, id))
}

// CloseAlert ends an alert at time ended (0 = now). Closing an ended alert changes nothing.
func (s *Store) CloseAlert(id, ended int64) error {
	if ended == 0 {
		ended = time.Now().Unix()
	}
	_, err := s.db.Exec("UPDATE alerts SET ended = ? WHERE id = ? AND ended = 0", ended, id)
	return err
}

// UnackAlert removes the acknowledgement of an alert (it got worse).
func (s *Store) UnackAlert(id int64) error {
	return affected(s.db.Exec("UPDATE alerts SET acked_by = '', acked_at = 0 WHERE id = ?", id))
}

// AckAlert marks an alert as acknowledged by user at time at (0 = now).
func (s *Store) AckAlert(id int64, user string, at int64) error {
	if at == 0 {
		at = time.Now().Unix()
	}
	return affected(s.db.Exec("UPDATE alerts SET acked_by = ?, acked_at = ? WHERE id = ?", user, at, id))
}

func (s *Store) queryAlerts(ctx context.Context, q string, args ...any) ([]model.Alert, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+alertCols+" FROM alerts "+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Alert{}
	for rows.Next() {
		var a model.Alert
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.DeviceName, &a.CheckKey, &a.CheckName, &a.Severity, &a.Message,
			&a.Started, &a.Ended, &a.AckedBy, &a.AckedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActiveAlerts returns all alerts that are not ended, newest first.
func (s *Store) ActiveAlerts() ([]model.Alert, error) {
	return s.queryAlerts(context.Background(), "WHERE ended = 0 ORDER BY id DESC")
}

// AlertQuery filters Alerts.
type AlertQuery struct {
	DeviceID   string // empty = all devices
	ActiveOnly bool
	Before     int64 // only alerts with a smaller id (paging), 0 = newest
	Limit      int   // default 100, at most 1000
}

// Alerts lists alerts newest first.
func (s *Store) Alerts(ctx context.Context, q AlertQuery) ([]model.Alert, error) {
	where := "WHERE 1=1"
	args := []any{}
	if q.DeviceID != "" {
		where += " AND device_id = ?"
		args = append(args, q.DeviceID)
	}
	if q.ActiveOnly {
		where += " AND ended = 0"
	}
	if q.Before > 0 {
		where += " AND id < ?"
		args = append(args, q.Before)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, min(limit, 1000))
	return s.queryAlerts(ctx, where+" ORDER BY id DESC LIMIT ?", args...)
}

// PruneAlerts deletes alerts that ended before olderThan (unix seconds). Active alerts stay.
func (s *Store) PruneAlerts(olderThan int64) error {
	_, err := s.db.Exec("DELETE FROM alerts WHERE ended > 0 AND ended < ?", olderThan)
	return err
}

// ---- device statuses ----

// SaveStatus persists the runtime status of a device.
func (s *Store) SaveStatus(st model.DeviceStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO statuses (device_id, data) VALUES (?, ?) ON CONFLICT (device_id) DO UPDATE SET data = excluded.data",
		st.DeviceID, string(b))
	return err
}

// LoadStatuses returns all persisted statuses by device id.
func (s *Store) LoadStatuses() (map[string]model.DeviceStatus, error) {
	rows, err := s.db.Query("SELECT device_id, data FROM statuses")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.DeviceStatus{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var st model.DeviceStatus
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return nil, fmt.Errorf("status %s: %w", id, err)
		}
		st.DeviceID = id
		out[id] = st
	}
	return out, rows.Err()
}

// DeleteStatus removes the persisted status of a device.
func (s *Store) DeleteStatus(deviceID string) error {
	_, err := s.db.Exec("DELETE FROM statuses WHERE device_id = ?", deviceID)
	return err
}
