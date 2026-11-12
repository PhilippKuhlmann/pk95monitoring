// Package fakesnmp simulates SNMP devices (switch, UniFi switch, UPS, printer, Synology NAS)
// in memory.
// A Device implements snmp.Session through Dialer; its values move with time (sine plus
// noise, counters grow with the traffic), so the demo mode shows live charts and tests
// can poll twice with a controlled clock.
package fakesnmp

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
)

// ErrTimeout is returned for unknown hosts, unreachable devices and a wrong community.
var ErrTimeout = errors.New("request timeout (after 1 retries)")

// Device is one simulated SNMP agent. All methods are safe for concurrent use.
type Device struct {
	// Community the device answers to ("" = any). Set it before the device is used.
	Community string

	mu          sync.Mutex
	clock       func() time.Time
	start       time.Time
	seed        uint64
	baseUptime  float64 // seconds the device was up before start
	build       func(t float64, m mib)
	ports       []*port
	unreachable bool
	over        map[string]gosnmp.SnmpPDU
	hidden      []string

	ups     upsState
	printer printerState
	nas     nasState
	sw      switchState
}

func newDevice(name string) *Device {
	h := fnv.New64a()
	h.Write([]byte(name))
	seed := h.Sum64()
	return &Device{
		clock:      time.Now,
		start:      time.Now(),
		seed:       seed,
		baseUptime: float64(86400*(3+seed%40) + seed%86400),
		over:       map[string]gosnmp.SnmpPDU{},
	}
}

// SetClock replaces the clock (default time.Now). The simulation restarts at the new
// clock's current time.
func (d *Device) SetClock(now func() time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ups.capAtSwi = d.capacity(d.elapsed()) // keep the charge, restart its timeline
	d.ups.switchAt = 0
	d.clock = now
	d.start = now()
	for _, p := range d.ports {
		p.downAt, p.lostRx, p.lostTx, p.lostUp = 0, 0, 0, 0
	}
}

// SetReachable makes the device answer (true, default) or time out (false).
func (d *Device) SetReachable(ok bool) {
	d.mu.Lock()
	d.unreachable = !ok
	d.mu.Unlock()
}

// Set overrides one object with a fixed value (e.g. a counter near its wrap or a custom
// OID). Typical types: gosnmp.Integer (int), Counter32/Gauge32 (uint), Counter64
// (uint64), TimeTicks (uint32), OctetString ([]byte or string).
func (d *Device) Set(oid string, typ gosnmp.Asn1BER, value any) {
	oid = strings.TrimPrefix(oid, ".")
	if s, ok := value.(string); ok && typ == gosnmp.OctetString {
		value = []byte(s)
	}
	d.mu.Lock()
	d.over[oid] = gosnmp.SnmpPDU{Name: "." + oid, Type: typ, Value: value}
	d.mu.Unlock()
}

// Unset removes an override set with Set.
func (d *Device) Unset(oid string) {
	d.mu.Lock()
	delete(d.over, strings.TrimPrefix(oid, "."))
	d.mu.Unlock()
}

// Hide removes a whole subtree (simulates a MIB the device does not implement).
// Overrides below it stay visible.
func (d *Device) Hide(prefix string) {
	d.mu.Lock()
	d.hidden = append(d.hidden, strings.TrimPrefix(prefix, "."))
	d.mu.Unlock()
}

// elapsed returns the seconds since start; the caller holds d.mu.
func (d *Device) elapsed() float64 {
	return max(d.clock().Sub(d.start).Seconds(), 0)
}

// snapshot builds all objects of the device at the current time.
func (d *Device) snapshot() (mib, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unreachable {
		return nil, ErrTimeout
	}
	m := mib{}
	t := d.elapsed()
	if d.build != nil {
		d.build(t, m)
	}
	for _, h := range d.hidden {
		for oid := range m {
			if oid == h || strings.HasPrefix(oid, h+".") {
				delete(m, oid)
			}
		}
	}
	for oid, p := range d.over {
		m[oid] = p
	}
	return m, nil
}

// Get implements snmp.Session semantics for one device.
func (d *Device) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	m, err := d.snapshot()
	if err != nil {
		return nil, err
	}
	out := make([]gosnmp.SnmpPDU, 0, len(oids))
	for _, oid := range oids {
		oid = strings.TrimPrefix(oid, ".")
		if p, ok := m[oid]; ok {
			out = append(out, p)
		} else {
			out = append(out, gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.NoSuchObject})
		}
	}
	return out, nil
}

// Walk returns every object below root (and root itself) in OID order.
func (d *Device) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	m, err := d.snapshot()
	if err != nil {
		return nil, err
	}
	root = strings.TrimPrefix(root, ".")
	var keys []string
	for oid := range m {
		if oid == root || strings.HasPrefix(oid, root+".") {
			keys = append(keys, oid)
		}
	}
	slices.SortFunc(keys, snmp.CompareOID)
	out := make([]gosnmp.SnmpPDU, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out, nil
}

// session is an open connection to a device.
type session struct {
	d       *Device
	refused bool // wrong community: every request times out
}

func (s *session) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	if s.refused {
		return nil, ErrTimeout
	}
	return s.d.Get(oids)
}

func (s *session) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	if s.refused {
		return nil, ErrTimeout
	}
	return s.d.Walk(root)
}

func (s *session) Close() error { return nil }

// Dialer returns a DialFunc that connects to the devices by host. Unknown hosts time out.
// The map must not be modified while the DialFunc is in use.
func Dialer(devs map[string]*Device) snmp.DialFunc {
	return func(ctx context.Context, host string, cfg model.SNMPConfig, _ time.Duration) (snmp.Session, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := devs[host]
		if d == nil {
			return nil, ErrTimeout
		}
		cfg = snmp.WithDefaults(cfg)
		refused := cfg.Version != "3" && d.Community != "" && cfg.Community != d.Community
		return &session{d: d, refused: refused}, nil
	}
}

// ---- object helpers ----

// mib is a snapshot of all objects keyed by OID without leading dot.
type mib map[string]gosnmp.SnmpPDU

func (m mib) put(oid string, typ gosnmp.Asn1BER, v any) {
	m[oid] = gosnmp.SnmpPDU{Name: "." + oid, Type: typ, Value: v}
}

func (m mib) str(oid, s string)          { m.put(oid, gosnmp.OctetString, []byte(s)) }
func (m mib) bytes(oid string, b []byte) { m.put(oid, gosnmp.OctetString, b) }
func (m mib) int(oid string, v int)      { m.put(oid, gosnmp.Integer, v) }
func (m mib) gauge(oid string, v float64) {
	m.put(oid, gosnmp.Gauge32, uint(math.Max(0, math.Min(v, math.MaxUint32))))
}
func (m mib) c32(oid string, v float64) { m.put(oid, gosnmp.Counter32, uint(uint64(v)&math.MaxUint32)) }
func (m mib) c64(oid string, v float64) { m.put(oid, gosnmp.Counter64, uint64(v)) }
func (m mib) ticks(oid string, sec float64) {
	m.put(oid, gosnmp.TimeTicks, uint32(uint64(sec*100)&math.MaxUint32))
}
func (m mib) oid(oid, v string) { m.put(oid, gosnmp.ObjectIdentifier, "."+v) }

// ---- value helpers ----

// wave is a deterministic oscillation in [-1, 1] with the given period (seconds).
func (d *Device) wave(t, period float64, salt uint64) float64 {
	phase := float64((d.seed^salt)%1000) / 1000 * 2 * math.Pi
	return math.Sin(2*math.Pi*t/period + phase)
}

// noise is deterministic noise in [-1, 1] that changes every 10 seconds.
func (d *Device) noise(t float64, salt uint64) float64 {
	x := d.seed ^ (salt * 0x9e3779b97f4a7c15) ^ uint64(t/10)*0xbf58476d1ce4e5b9
	x ^= x >> 31
	x *= 0x94d049bb133111eb
	x ^= x >> 29
	return float64(x%20001)/10000 - 1
}

// integral returns the bytes transferred until t at a rate of avg·(1 + 0.6·sin(ωt+φ)).
// The rate never drops below 0.4·avg, so the counter grows strictly.
func integral(avg, period, phase, t float64) float64 {
	w := 2 * math.Pi / period
	return avg * (t - 0.6/w*(math.Cos(w*t+phase)-math.Cos(phase)))
}
