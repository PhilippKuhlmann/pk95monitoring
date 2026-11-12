package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
	"pk95monitoring/web"
)

func TestSparkWindow(t *testing.T) {
	const now = 1_700_000_123
	for _, tc := range []struct {
		span     int64
		interval int
		step     int64
		n        int
	}{
		{3600, 60, 75, 48},
		{6 * 3600, 60, 450, 48},
		{24 * 3600, 60, 1800, 48},
		{3600, 300, 300, 12},    // slow device: one bucket per poll
		{3600, 90, 120, 30},     // poll interval rounded up to whole minutes
		{6 * 3600, 30, 450, 48}, // fast device: still 48 buckets
	} {
		from, step, n := sparkWindow(now, tc.span, tc.interval)
		if step != tc.step || n != tc.n {
			t.Errorf("span %d interval %d: step %d n %d, want %d %d", tc.span, tc.interval, step, n, tc.step, tc.n)
		}
		last := from + int64(n-1)*step
		if from%step != 0 || now < last || now >= last+step || now-from < tc.span-step {
			t.Errorf("span %d: from %d last %d now %d step %d", tc.span, from, last, now, step)
		}
	}
}

func TestSparkValuesJSON(t *testing.T) {
	b, err := json.Marshal(map[string]any{"v": sparkValues{1.5, math.NaN(), 1234567.891, 0, -0.000012345678, math.Inf(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":[1.5,null,1.23457e+06,0,-1.23457e-05,null]}` {
		t.Errorf("got %s", b)
	}
	var back struct{ V []*float64 }
	if err := json.Unmarshal(b, &back); err != nil || len(back.V) != 6 || back.V[1] != nil || *back.V[2] != 1234570 {
		t.Errorf("round trip %v %v", back, err)
	}
	if b, _ := json.Marshal(sparkValues{}); string(b) != "[]" {
		t.Errorf("empty: %s", b)
	}
}

func TestSparkBuckets(t *testing.T) {
	pts := []store.Point{{TS: 900, Avg: 9}, {TS: 1000, Avg: 1}, {TS: 1200, Avg: 3}, {TS: 1400, Avg: 5}}
	v := sparkBuckets(pts, 1000, 100, 4)
	if len(v) != 4 || v[0] != 1 || !math.IsNaN(v[1]) || v[2] != 3 || !math.IsNaN(v[3]) {
		t.Errorf("buckets %v", v)
	}
}

func TestSparkSelect(t *testing.T) {
	var list []store.Series
	for i := range maxSparkSeries + 5 {
		// Name order differs from the update order: the five oldest are spread over the list.
		list = append(list, store.Series{ID: int64(i + 1), Name: fmt.Sprintf("m%04d", i), LastTS: int64(1000 + (i*7919)%(maxSparkSeries+5))})
	}
	got, capped := sparkSelect(list)
	if !capped || len(got) != maxSparkSeries {
		t.Fatalf("capped %v len %d", capped, len(got))
	}
	if !slices.IsSortedFunc(got, func(a, b store.Series) int { return cmp.Compare(a.ID, b.ID) }) {
		t.Error("order of the list not kept")
	}
	for _, se := range got {
		if se.LastTS < 1005 {
			t.Errorf("old series kept: %+v", se)
		}
	}
	if got, capped := sparkSelect(list[:10]); capped || len(got) != 10 {
		t.Errorf("small list: %v %d", capped, len(got))
	}
}

// sparkFixture is a core on a temporary store with a fixed clock (core.Run is not started) and
// a server whose session store accepts the cookie "good".
func sparkFixture(t *testing.T, now int64) (*core.Core, *Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := core.New(st, core.Options{Version: "1.2.3", DataDir: t.TempDir(), Instance: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Unix(now, 0) }
	s := New(c, web.FS, Options{})
	s.auths = stubAuth{}
	return c, s
}

func sparkDevice(t *testing.T, c *core.Core, name string) string {
	t.Helper()
	d, errs, err := c.SaveDevice(model.Device{Name: name, Type: model.TypeNet, Host: "127.0.0.1",
		Checks: []model.NetCheck{{Type: model.NetPing}}})
	if err != nil || len(errs) > 0 {
		t.Fatalf("save device: %v %v", errs, err)
	}
	return d.ID
}

type sparkResp struct {
	From, To, Step int64
	Capped         *bool
	Series         []struct {
		ID       int64      `json:"id"`
		CheckKey string     `json:"check_key"`
		Name     string     `json:"name"`
		Instance string     `json:"instance"`
		Unit     string     `json:"unit"`
		Last     *float64   `json:"last"`
		LastTS   int64      `json:"last_ts"`
		Values   []*float64 `json:"values"`
	}
}

func getSparks(t *testing.T, s *Server, path string, cookie bool) (int, sparkResp) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if cookie {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: "good"})
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	var out sparkResp
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestSparksEndpoint(t *testing.T) {
	const now = 1_700_000_123
	c, s := sparkFixture(t, now)
	id := sparkDevice(t, c, "Server")
	other := sparkDevice(t, c, "Anderes")

	// cpu.usage every 60 s for 6 h, except a gap from now-3h to now-2h; mem.usage only once.
	var cpuTS []int64
	for ts := int64(now - 6*3600 + 30); ts <= now; ts += 60 {
		checks := []model.Check{{Key: "net:x", Name: "x", State: "ok", Metrics: []model.Metric{{Name: "ping.rtt", Value: 1, Unit: "ms"}}}}
		if ts < now-3*3600 || ts >= now-2*3600 {
			cpuTS = append(cpuTS, ts)
			checks = append(checks, model.Check{Key: "cpu", Name: "CPU", State: "ok", Metrics: []model.Metric{{Name: "cpu.usage", Value: 50, Unit: "%"}}})
		}
		if err := c.St.WriteMetrics(id, ts, checks); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.St.WriteMetrics(id, now, []model.Check{{Key: "mem", Name: "Arbeitsspeicher", State: "ok",
		Metrics: []model.Metric{{Name: "mem.usage", Value: 42.25, Unit: "%"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.St.WriteMetrics(other, now-100, []model.Check{{Key: "cpu", Name: "CPU", State: "ok",
		Metrics: []model.Metric{{Name: "cpu.usage", Value: 99, Unit: "%"}}}}); err != nil {
		t.Fatal(err)
	}

	// auth, unknown device, unknown range
	if code, _ := getSparks(t, s, "/api/devices/"+id+"/sparks", false); code != 401 {
		t.Errorf("without session: %d", code)
	}
	if code, _ := getSparks(t, s, "/api/devices/nope/sparks", true); code != 404 {
		t.Errorf("unknown device: %d", code)
	}
	for _, rg := range []string{"7d", "2h", "x"} {
		if code, _ := getSparks(t, s, "/api/devices/"+id+"/sparks?range="+rg, true); code != 400 {
			t.Errorf("range %s: %d", rg, code)
		}
	}

	for _, tc := range []struct {
		query string
		step  int64
	}{{"", 450}, {"?range=6h", 450}, {"?range=1h", 75}, {"?range=24h", 1800}} {
		code, res := getSparks(t, s, "/api/devices/"+id+"/sparks"+tc.query, true)
		if code != 200 || res.Step != tc.step || res.To != now || res.Capped == nil || *res.Capped || res.From%tc.step != 0 {
			t.Fatalf("%q: %d step %d to %d capped %v from %d", tc.query, code, res.Step, res.To, res.Capped, res.From)
		}
		if len(res.Series) != 3 {
			t.Fatalf("%q: %d series %+v", tc.query, len(res.Series), res.Series)
		}
		byName := map[string]int{}
		for i, se := range res.Series {
			byName[se.Name] = i
			if len(se.Values) != 48 {
				t.Errorf("%q %s: %d values", tc.query, se.Name, len(se.Values))
			}
		}
		cpu := res.Series[byName["cpu.usage"]]
		if cpu.CheckKey != "cpu" || cpu.Unit != "%" || cpu.Last == nil || *cpu.Last != 50 || cpu.LastTS != cpuTS[len(cpuTS)-1] {
			t.Errorf("%q cpu %+v", tc.query, cpu)
		}
		// A bucket has a value exactly when a sample falls into it.
		nulls := 0
		for i, v := range cpu.Values {
			b := res.From + int64(i)*res.Step
			want := slices.ContainsFunc(cpuTS, func(ts int64) bool { return ts >= b && ts < b+res.Step })
			if (v != nil) != want || (v != nil && *v != 50) {
				t.Errorf("%q cpu bucket %d (%d): %v, want value %v", tc.query, i, b, v, want)
			}
			if v == nil {
				nulls++
			}
		}
		if tc.step == 450 && nulls < 6 {
			t.Errorf("6h: gap not visible, %d nulls", nulls)
		}
		mem := res.Series[byName["mem.usage"]]
		if mem.CheckKey != "mem" || mem.Last == nil || *mem.Last != 42.25 || mem.Values[47] == nil || *mem.Values[47] != 42.25 || mem.Values[0] != nil {
			t.Errorf("%q mem %+v", tc.query, mem)
		}
	}

	// A device without series answers an empty list.
	empty := sparkDevice(t, c, "Leer")
	if code, res := getSparks(t, s, "/api/devices/"+empty+"/sparks", true); code != 200 || res.Series == nil || len(res.Series) != 0 {
		t.Errorf("empty device: %d %+v", code, res)
	}
}

func TestSparksCapped(t *testing.T) {
	const now = 1_700_000_123
	c, s := sparkFixture(t, now)
	id := sparkDevice(t, c, "Switch")
	n := maxSparkSeries + 5
	for i := range n {
		// Series i was last updated at now - (n-i) * 10: the first five are the oldest.
		m := model.Metric{Name: "if.rx", Instance: fmt.Sprintf("port%04d", i), Value: float64(i), Unit: "B/s"}
		if err := c.St.WriteMetrics(id, now-int64(n-i)*10, []model.Check{{Key: "snmp:if:" + m.Instance, Name: "Port", State: "ok", Metrics: []model.Metric{m}}}); err != nil {
			t.Fatal(err)
		}
	}
	code, res := getSparks(t, s, "/api/devices/"+id+"/sparks", true)
	if code != 200 || res.Capped == nil || !*res.Capped || len(res.Series) != maxSparkSeries {
		t.Fatalf("%d capped %v len %d", code, res.Capped, len(res.Series))
	}
	if res.Series[0].Instance != "port0005" || res.Series[len(res.Series)-1].Instance != fmt.Sprintf("port%04d", n-1) {
		t.Errorf("kept %s … %s", res.Series[0].Instance, res.Series[len(res.Series)-1].Instance)
	}
	if i := slices.IndexFunc(res.Series[0].Values, func(v *float64) bool { return v != nil }); i < 0 || *res.Series[0].Values[i] != 5 {
		t.Errorf("values of the first kept series: %v", res.Series[0].Values)
	}
}

// TestSparksPerformance answers the sparklines of a device with 600 series and 6 h of samples
// every 60 s (216 000 samples) from a temporary store.
func TestSparksPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const series, now = 600, 1_700_000_123
	c, s := sparkFixture(t, now)
	id := sparkDevice(t, c, "Großer Switch")
	start := time.Now()
	for ts := int64(now - 6*3600 + 60); ts <= now; ts += 60 {
		ms := make([]model.Metric, series)
		for i := range ms {
			ms[i] = model.Metric{Name: "if.rx", Instance: fmt.Sprintf("port%03d", i), Value: float64(i + int(ts%7)), Unit: "B/s"}
		}
		if err := c.St.WriteMetrics(id, ts, []model.Check{{Key: "snmp:if", Name: "Ports", State: "ok", Metrics: ms}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d series × 6 h in %v", series, time.Since(start).Round(time.Millisecond))

	for _, rg := range []string{"6h", "1h", "24h"} {
		var times []time.Duration
		for range 5 {
			t0 := time.Now()
			code, res := getSparks(t, s, "/api/devices/"+id+"/sparks?range="+rg, true)
			times = append(times, time.Since(t0))
			if code != 200 || len(res.Series) != series || *res.Capped {
				t.Fatalf("%s: %d %d", rg, code, len(res.Series))
			}
			for _, se := range res.Series {
				if len(se.Values) != 48 || se.Values[47] == nil {
					t.Fatalf("%s %s: %d values, last %v", rg, se.Instance, len(se.Values), se.Values[47])
				}
			}
		}
		first := times[0]
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		median := times[len(times)/2]
		t.Logf("range %s: first %v, median %v (race detector %v)", rg, first.Round(time.Millisecond), median.Round(time.Millisecond), raceEnabled)
		if rg == "6h" && !raceEnabled && median > 300*time.Millisecond {
			t.Errorf("6h sparks of %d series took %v, want < 300 ms", series, median)
		}
	}
}
