package netcheck

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"pk95monitoring/internal/model"
)

func TestPingLocalhost(t *testing.T) {
	c := runCheck(model.NetCheck{Type: model.NetPing, Target: "127.0.0.1", Timeout: 5})
	if c.State == model.StateUnknown && strings.Contains(c.Message, "nicht erlaubt") {
		t.Skip("ping not permitted on this machine:", c.Message)
	}
	expectState(t, c, model.StateOK, "4/4 Antworten")
	if loss, _ := metric(c, "ping.loss"); loss != 0 {
		t.Fatalf("ping.loss = %v", loss)
	}
	if _, ok := metric(c, "ping.rtt"); !ok {
		t.Fatal("ping.rtt missing")
	}
	if c.Name != "Ping 127.0.0.1" {
		t.Fatalf("Name = %q", c.Name)
	}
}

func TestPingResult(t *testing.T) {
	tests := []struct {
		name       string
		sent, recv int
		state, msg string
		loss       float64
		rtt        bool
	}{
		{"all", 4, 4, model.StateOK, "4/4 Antworten, Ø 12 ms", 0, true},
		{"partial", 4, 3, model.StateOK, "3/4 Antworten (25 % Verlust)", 25, true},
		{"none", 4, 0, model.StateCrit, "Keine Antwort", 100, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c model.Check
			pingResult(&c, &probing.Statistics{PacketsSent: tt.sent, PacketsRecv: tt.recv, AvgRtt: 12 * time.Millisecond})
			expectState(t, c, tt.state, tt.msg)
			if loss, _ := metric(c, "ping.loss"); loss != tt.loss {
				t.Errorf("ping.loss = %v, want %v", loss, tt.loss)
			}
			rtt, ok := metric(c, "ping.rtt")
			if ok != tt.rtt || ok && rtt != 12 {
				t.Errorf("ping.rtt = %v (%v)", rtt, ok)
			}
		})
	}
}

func TestPingErrors(t *testing.T) {
	old := pingFunc
	t.Cleanup(func() { pingFunc = old })

	tests := []struct {
		name  string
		err   error
		state string
		msg   string
	}{
		{"denied", errPingDenied, model.StateUnknown, "Ping nicht erlaubt (CAP_NET_RAW oder net.ipv4.ping_group_range setzen)"},
		{"other", &net.OpError{Op: "listen", Err: net.UnknownNetworkError("x")}, model.StateUnknown, "Ping fehlgeschlagen"},
		{"canceled", context.Canceled, model.StateUnknown, "abgebrochen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pingFunc = func(context.Context, *net.IPAddr) (*probing.Statistics, error) { return nil, tt.err }
			c := Run(context.Background(), model.NetCheck{ID: "p", Type: model.NetPing}, "192.0.2.1")
			expectState(t, c, tt.state, tt.msg)
		})
	}
}

// TestPingExpiredContext runs the real pinger with a context whose deadline has passed:
// pro-bing panics on a non-positive timeout, so the check must not reach it.
func TestPingExpiredContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c := Run(ctx, model.NetCheck{ID: "p", Type: model.NetPing, Target: "127.0.0.1"}, "")
	if c.State != model.StateUnknown {
		t.Fatalf("state %q msg %q, want unknown", c.State, c.Message)
	}
}
