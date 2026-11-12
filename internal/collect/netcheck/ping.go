package netcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"pk95monitoring/internal/model"
)

const (
	pingCount    = 4
	pingInterval = 250 * time.Millisecond
)

// errPingDenied means neither unprivileged (UDP ICMP) nor raw ICMP sockets may be opened.
var errPingDenied = errors.New("ping not permitted")

// pingFunc sends the echo requests; replaced in tests.
var pingFunc = ping

// runPing sends pingCount echo requests. It first tries unprivileged ICMP sockets (Linux:
// net.ipv4.ping_group_range, the default in Docker) and falls back to raw sockets
// (CAP_NET_RAW).
func runPing(ctx context.Context, res *model.Check, target string, timeout time.Duration) {
	ip, err := resolveIP(ctx, target)
	if err != nil {
		res.State, res.Message = model.StateCrit, errText(err, timeout)
		return
	}
	st, err := pingFunc(ctx, ip)
	switch {
	case errors.Is(err, errPingDenied):
		res.State, res.Message = model.StateUnknown, "Ping nicht erlaubt (CAP_NET_RAW oder net.ipv4.ping_group_range setzen)"
		return
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// No time was left to ping at all: not a fault of the target.
		res.State, res.Message = model.StateUnknown, errText(err, timeout)
		return
	case err != nil:
		res.State, res.Message = model.StateUnknown, "Ping fehlgeschlagen: "+err.Error()
		return
	case st == nil || st.PacketsSent == 0:
		res.State, res.Message = model.StateUnknown, "Ping fehlgeschlagen: keine Pakete gesendet"
		return
	}
	pingResult(res, st)
}

// pingResult turns the statistics into state, message and metrics.
func pingResult(res *model.Check, st *probing.Statistics) {
	recv := min(st.PacketsRecv, st.PacketsSent)
	loss := float64(st.PacketsSent-recv) / float64(st.PacketsSent) * 100
	res.Metrics = append(res.Metrics, model.Metric{Name: "ping.loss", Value: loss, Unit: "%"})
	if recv == 0 {
		res.State = model.StateCrit
		res.Message = fmt.Sprintf("Keine Antwort (%d Pakete gesendet)", st.PacketsSent)
		return
	}
	res.Metrics = append(res.Metrics, model.Metric{Name: "ping.rtt", Value: ms(st.AvgRtt), Unit: "ms"})
	res.State = model.StateOK
	if recv == st.PacketsSent {
		res.Message = fmt.Sprintf("%d/%d Antworten, Ø %s", recv, st.PacketsSent, fmtMs(st.AvgRtt))
	} else {
		res.Message = fmt.Sprintf("%d/%d Antworten (%.0f %% Verlust), Ø %s", recv, st.PacketsSent, loss, fmtMs(st.AvgRtt))
	}
}

// resolveIP resolves a ping target, preferring IPv4.
func resolveIP(ctx context.Context, host string) (*net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return &net.IPAddr{IP: ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return &a, nil
		}
	}
	return &addrs[0], nil
}

// ping runs pro-bing, first unprivileged, then privileged on a permission error.
func ping(ctx context.Context, ip *net.IPAddr) (*probing.Statistics, error) {
	st, err := pingOnce(ctx, ip, false)
	if err != nil && errors.Is(err, os.ErrPermission) {
		st, err = pingOnce(ctx, ip, true)
		if err != nil && errors.Is(err, os.ErrPermission) {
			return nil, errPingDenied
		}
	}
	return st, err
}

func pingOnce(ctx context.Context, ip *net.IPAddr, privileged bool) (*probing.Statistics, error) {
	// pro-bing panics (in a goroutine, killing the process) on a non-positive Timeout.
	left := time.Until(deadline(ctx))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if left < time.Millisecond {
		return nil, context.DeadlineExceeded
	}
	p := probing.New(ip.String())
	p.SetIPAddr(ip)
	p.SetPrivileged(privileged)
	p.SetLogger(probing.NoopLogger{})
	p.Count = pingCount
	p.Interval = pingInterval
	p.Timeout = left
	p.RecordRtts = false
	p.RecordTTLs = false
	if err := p.RunWithContext(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return p.Statistics(), nil
}

// deadline returns the context deadline or the default timeout from now.
func deadline(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(defaultTimeout)
}
