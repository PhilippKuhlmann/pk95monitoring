// Package docker turns the containers of a Docker (or Podman) host into checks: one check for
// the engine and one per container (running, healthcheck, restart loops, CPU, memory and
// network). The Linux agent reads the Engine API over the local socket with Client; the demo
// builds a Report directly. Restarts per hour and network rates need two readings, which
// Tracker keeps between queries.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pk95monitoring/internal/model"
)

// Check keys and group.
const (
	KeyHost   = "docker"
	KeyCt     = "docker:ct:" // + container name (stable when Compose re-creates a container)
	groupName = "Docker"
)

// Sockets are tried in this order when DOCKER_HOST does not name a unix socket.
var Sockets = []string{"/var/run/docker.sock", "/run/podman/podman.sock"}

// ErrDown means the socket exists but nobody answers: the engine is stopped.
var ErrDown = errors.New("Docker-Dienst antwortet nicht")

// Socket returns the engine socket of this host ("" without Docker or Podman).
func Socket() string {
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		if p := strings.TrimPrefix(h, "unix://"); isSocket(p) {
			return p
		}
	}
	for _, p := range Sockets {
		if isSocket(p) {
			return p
		}
	}
	return ""
}

func isSocket(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// Report is what Client.Query reads.
type Report struct {
	Available bool        `json:"available"`
	Engine    string      `json:"engine"` // "Docker" or "Podman"
	Version   string      `json:"version"`
	API       string      `json:"api"`
	Driver    string      `json:"driver"`
	Root      string      `json:"root"`
	NCPU      int         `json:"ncpu"`
	MemTotal  float64     `json:"mem_total"`
	Images    int         `json:"images"`
	Cts       []Container `json:"containers"`
}

// Container is one container. CPU, Mem* and Net* are set for running containers; Restarts1h,
// NetIn and NetOut are filled in by Tracker.
type Container struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`  // created, running, paused, restarting, removing, exited, dead
	Health    string `json:"health"` // healthy, unhealthy, starting or "" (no healthcheck)
	HealthOut string `json:"health_out"`
	Failing   int    `json:"failing"` // failed healthchecks in a row
	ExitCode  int    `json:"exit_code"`
	OOM       bool   `json:"oom"`
	Error     string `json:"error"`
	Created   int64  `json:"created"`  // unix
	Started   int64  `json:"started"`  // unix, 0 = never
	Finished  int64  `json:"finished"` // unix, 0 = never
	Policy    string `json:"policy"`   // restart policy: no, always, unless-stopped, on-failure
	Restarts  int    `json:"restarts"` // automatic restarts since creation
	Project   string `json:"project"`  // Compose project
	Service   string `json:"service"`  // Compose service
	OneOff    bool   `json:"one_off"`  // docker compose run
	Ports     string `json:"ports"`

	HasStats bool    `json:"has_stats"`
	CPU      float64 `json:"cpu"` // % of the host's CPUs (of the limit, if one is set)
	CPULimit float64 `json:"cpu_limit"`
	MemUsed  float64 `json:"mem_used"`
	MemLimit float64 `json:"mem_limit"` // the host's RAM without a limit
	NetRx    float64 `json:"net_rx"`    // bytes since start
	NetTx    float64 `json:"net_tx"`

	Restarts1h int     `json:"restarts_1h"`
	HasRates   bool    `json:"has_rates"`
	NetIn      float64 `json:"net_in"` // B/s
	NetOut     float64 `json:"net_out"`
}

// mustRun reports whether a stopped container is a problem: its restart policy keeps it
// running (always, unless-stopped), or it ended with an error although it is a service
// (on-failure, or part of a Compose project). One-off containers ("docker run" without
// restart policy, "docker compose run") may stop.
func (c Container) mustRun() bool {
	switch c.Policy {
	case "always", "unless-stopped":
		return true
	case "on-failure":
		return c.ExitCode != 0
	}
	return c.Project != "" && !c.OneOff && c.ExitCode != 0
}

// Client talks to the Engine API.
type Client struct {
	HTTP *http.Client
	Base string // "http://docker" for a socket
}

// NewSocket returns a client for a unix socket.
func NewSocket(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second,
	}
	return &Client{HTTP: &http.Client{Transport: tr}, Base: "http://docker"}
}

// Close drops idle connections.
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }

func (c *Client) get(ctx context.Context, path string, timeout time.Duration, v any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("%w (%v)", ErrDown, unwrapNet(err))
		}
		return unwrapNet(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Message)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

// unwrapNet drops the "Get http://docker/…" prefix of a transport error.
func unwrapNet(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type apiVersion struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Platform   struct {
		Name string `json:"Name"`
	} `json:"Platform"`
}

type apiInfo struct {
	Driver        string `json:"Driver"`
	DockerRootDir string `json:"DockerRootDir"`
	NCPU          int    `json:"NCPU"`
	MemTotal      int64  `json:"MemTotal"`
	Images        int    `json:"Images"`
}

type apiListItem struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	Created int64 `json:"Created"`
}

type apiInspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		OOMKilled  bool   `json:"OOMKilled"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
			Log           []struct {
				ExitCode int    `json:"ExitCode"`
				Output   string `json:"Output"`
			} `json:"Log"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		NanoCpus      int64 `json:"NanoCpus"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

type apiStats struct {
	CPU    cpuStats `json:"cpu_stats"`
	PreCPU cpuStats `json:"precpu_stats"`
	Memory struct {
		Usage float64            `json:"usage"`
		Limit float64            `json:"limit"`
		Stats map[string]float64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		Rx float64 `json:"rx_bytes"`
		Tx float64 `json:"tx_bytes"`
	} `json:"networks"`
}

type cpuStats struct {
	Usage struct {
		Total  float64   `json:"total_usage"`
		PerCPU []float64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	System float64 `json:"system_cpu_usage"`
	Online int     `json:"online_cpus"`
}

// statsWorkers bounds the parallel stats requests (each takes about a second: the engine
// samples twice to compute the CPU usage).
const statsWorkers = 8

// Query reads the engine and all containers.
func (c *Client) Query(ctx context.Context) (Report, error) {
	var v apiVersion
	if err := c.get(ctx, "/version", 10*time.Second, &v); err != nil {
		return Report{}, err
	}
	var info apiInfo
	if err := c.get(ctx, "/info", 15*time.Second, &info); err != nil {
		return Report{}, err
	}
	var list []apiListItem
	if err := c.get(ctx, "/containers/json?all=1", 15*time.Second, &list); err != nil {
		return Report{}, err
	}
	r := Report{Available: true, Engine: "Docker", Version: v.Version, API: v.APIVersion, Driver: info.Driver,
		Root: info.DockerRootDir, NCPU: info.NCPU, MemTotal: float64(info.MemTotal), Images: info.Images}
	if strings.Contains(strings.ToLower(v.Platform.Name), "podman") {
		r.Engine = "Podman"
	}
	r.Cts = make([]Container, len(list))
	for i, it := range list {
		r.Cts[i] = fromList(it)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, statsWorkers)
	for i := range r.Cts {
		wg.Add(1)
		go func(ct *Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var in apiInspect
			if err := c.get(ctx, "/containers/"+ct.ID+"/json", 10*time.Second, &in); err == nil {
				applyInspect(ct, in)
			}
			if ct.State != "running" {
				return
			}
			var st apiStats
			if err := c.get(ctx, "/containers/"+ct.ID+"/stats?stream=false", 15*time.Second, &st); err == nil {
				applyStats(ct, st, r.MemTotal)
			}
		}(&r.Cts[i])
	}
	wg.Wait()
	return r, nil
}

func fromList(it apiListItem) Container {
	ct := Container{ID: it.ID, Image: it.Image, State: it.State, Created: it.Created}
	if len(it.Names) > 0 {
		ct.Name = strings.TrimPrefix(it.Names[0], "/")
	}
	if ct.Name == "" {
		ct.Name = shortID(it.ID)
	}
	ct.Project = it.Labels["com.docker.compose.project"]
	ct.Service = it.Labels["com.docker.compose.service"]
	ct.OneOff = strings.EqualFold(it.Labels["com.docker.compose.oneoff"], "true")
	var ports []string
	seen := map[string]bool{}
	for _, p := range it.Ports {
		if p.PublicPort == 0 {
			continue
		}
		s := fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		if !seen[s] {
			seen[s] = true
			ports = append(ports, s)
		}
	}
	slices.Sort(ports)
	ct.Ports = strings.Join(ports, ", ")
	return ct
}

func applyInspect(ct *Container, in apiInspect) {
	if in.State.Status != "" {
		ct.State = in.State.Status
	}
	ct.Restarts = in.RestartCount
	ct.ExitCode = in.State.ExitCode
	ct.OOM = in.State.OOMKilled
	ct.Error = in.State.Error
	ct.Started = parseTime(in.State.StartedAt)
	ct.Finished = parseTime(in.State.FinishedAt)
	ct.Policy = in.HostConfig.RestartPolicy.Name
	if ct.Policy == "" {
		ct.Policy = "no"
	}
	if in.HostConfig.NanoCpus > 0 {
		ct.CPULimit = float64(in.HostConfig.NanoCpus) / 1e9
	}
	if h := in.State.Health; h != nil && h.Status != "" && h.Status != "none" {
		ct.Health = h.Status
		ct.Failing = h.FailingStreak
		for i := len(h.Log) - 1; i >= 0; i-- {
			if h.Log[i].ExitCode != 0 {
				ct.HealthOut = truncate(strings.Join(strings.Fields(h.Log[i].Output), " "), 200)
				break
			}
		}
	}
}

func applyStats(ct *Container, st apiStats, hostMem float64) {
	cpuDelta := st.CPU.Usage.Total - st.PreCPU.Usage.Total
	sysDelta := st.CPU.System - st.PreCPU.System
	online := st.CPU.Online
	if online == 0 {
		online = len(st.CPU.Usage.PerCPU)
	}
	if cpuDelta >= 0 && sysDelta > 0 && st.PreCPU.System > 0 {
		pct := cpuDelta / sysDelta * 100 // share of all CPUs of the host
		if ct.CPULimit > 0 && online > 0 {
			pct = pct * float64(online) / ct.CPULimit
		}
		ct.CPU = min(round2(pct), 100)
		ct.HasStats = true
	}
	if st.Memory.Usage > 0 {
		used := st.Memory.Usage
		// as "docker stats": without the page cache that can be reclaimed
		if v, ok := st.Memory.Stats["inactive_file"]; ok && v < used { // cgroup v2
			used -= v
		} else if v, ok := st.Memory.Stats["total_inactive_file"]; ok && v < used { // cgroup v1
			used -= v
		}
		ct.MemUsed = used
		ct.MemLimit = st.Memory.Limit
		if ct.MemLimit <= 0 || (hostMem > 0 && ct.MemLimit > hostMem) {
			ct.MemLimit = hostMem
		}
		ct.HasStats = true
	}
	for _, n := range st.Networks {
		ct.NetRx += n.Rx
		ct.NetTx += n.Tx
	}
}

func parseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return 0
	}
	return t.Unix()
}

// Tracker remembers the previous reading of each container to count restarts within the last
// hour and to compute network rates. The agent keeps one for its lifetime; it also holds the
// last good report so the containers stay visible (unknown) while the engine does not answer.
type Tracker struct {
	mu       sync.Mutex
	prev     map[string]sample  // by container name
	restarts map[string][]int64 // by container name: unix times of observed restarts
	good     Report
}

type sample struct {
	id       string
	at       time.Time
	rx, tx   float64
	restarts int
	running  bool
}

// Update fills in Restarts1h and the network rates of r and remembers r.
func (t *Tracker) Update(r *Report, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.prev == nil {
		t.prev, t.restarts = map[string]sample{}, map[string][]int64{}
	}
	cut := now.Add(-time.Hour).Unix()
	next := map[string]sample{}
	for i := range r.Cts {
		c := &r.Cts[i]
		p, ok := t.prev[c.Name]
		if ok && p.id == c.ID {
			for n := c.Restarts - p.restarts; n > 0; n-- {
				t.restarts[c.Name] = append(t.restarts[c.Name], now.Unix())
			}
			if dt := now.Sub(p.at).Seconds(); dt > 0 && p.running && c.State == "running" && c.HasStats && c.NetRx >= p.rx && c.NetTx >= p.tx {
				c.NetIn, c.NetOut, c.HasRates = round2((c.NetRx-p.rx)/dt), round2((c.NetTx-p.tx)/dt), true
			}
		}
		times := t.restarts[c.Name][:0]
		for _, ts := range t.restarts[c.Name] {
			if ts > cut {
				times = append(times, ts)
			}
		}
		t.restarts[c.Name] = times
		c.Restarts1h = len(times)
		next[c.Name] = sample{id: c.ID, at: now, rx: c.NetRx, tx: c.NetTx, restarts: c.Restarts, running: c.State == "running" && c.HasStats}
	}
	for name := range t.restarts {
		if _, ok := next[name]; !ok {
			delete(t.restarts, name)
		}
	}
	t.prev = next
	t.good = *r
}

// Good returns the last report given to Update.
func (t *Tracker) Good() Report {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.good
}

// Checks builds the checks: docker (engine, how many containers run) and one docker:ct:<name>
// per container. A host without Docker has none. When the query failed, r is the last good
// report: its containers are reported unknown so their alerts stay open.
func Checks(r Report, err error, now time.Time) []model.Check {
	if err != nil {
		host := model.Check{Key: KeyHost, Name: engineName(r), Group: groupName, State: model.StateUnknown,
			Message: engineName(r) + " nicht lesbar: " + err.Error()}
		if errors.Is(err, ErrDown) {
			host.State, host.Message = model.StateCrit, engineName(r)+"-Dienst läuft nicht – der Socket antwortet nicht"
		}
		out := []model.Check{host}
		for _, c := range sorted(r.Cts) {
			out = append(out, model.Check{Key: KeyCt + c.Name, Name: "Container " + c.Name, Group: groupName,
				State: model.StateUnknown, Message: engineName(r) + " nicht erreichbar"})
		}
		return out
	}
	if !r.Available {
		return nil
	}
	cts := sorted(r.Cts)
	running := 0
	var rest []model.Check
	for _, c := range cts {
		if c.State == "running" {
			running++
		}
		rest = append(rest, ctCheck(c, now))
	}
	host := model.Check{Key: KeyHost, Name: engineName(r), Group: groupName, State: model.StateOK,
		Message: fmt.Sprintf("%s %s · %d von %d %s laufen", r.Engine, r.Version, running, len(cts), plural(len(cts), "Container", "Containern")),
		Facts:   map[string]string{"Container": strconv.Itoa(len(cts)), "Laufen": strconv.Itoa(running), "Images": strconv.Itoa(r.Images)}}
	if len(cts) == 1 {
		host.Message = fmt.Sprintf("%s %s · %d von 1 Container läuft", r.Engine, r.Version, running)
	}
	if r.Version != "" {
		host.Facts["Version"] = r.Version
	}
	if r.API != "" {
		host.Facts["API"] = r.API
	}
	if r.Driver != "" {
		host.Facts["Speichertreiber"] = r.Driver
	}
	if r.Root != "" {
		host.Facts["Datenverzeichnis"] = r.Root
	}
	return append([]model.Check{host}, rest...)
}

func engineName(r Report) string {
	if r.Engine != "" {
		return r.Engine
	}
	return "Docker"
}

func sorted(cts []Container) []Container {
	out := slices.Clone(cts)
	slices.SortFunc(out, func(a, b Container) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

var policyText = map[string]string{"no": "keine", "always": "immer", "unless-stopped": "außer manuell gestoppt", "on-failure": "bei Fehler"}

func ctCheck(c Container, now time.Time) model.Check {
	ch := model.Check{Key: KeyCt + c.Name, Name: "Container " + c.Name, Group: groupName, State: model.StateOK, Facts: map[string]string{}}
	inst := c.Name
	ch.Facts["Image"] = c.Image
	ch.Facts["ID"] = shortID(c.ID)
	if t, ok := policyText[c.Policy]; ok {
		ch.Facts["Neustart-Richtlinie"] = t
	}
	if c.Project != "" {
		ch.Facts["Compose-Projekt"] = c.Project
		if c.Service != "" {
			ch.Facts["Compose-Dienst"] = c.Service
		}
	}
	if c.Ports != "" {
		ch.Facts["Ports"] = c.Ports
	}
	if c.Created > 0 {
		ch.Facts["Erstellt"] = time.Unix(c.Created, 0).Local().Format("02.01.2006 15:04")
	}
	if c.Started > 0 {
		ch.Facts["Gestartet"] = time.Unix(c.Started, 0).Local().Format("02.01.2006 15:04")
	}
	if c.Restarts > 0 {
		ch.Facts["Neustarts gesamt"] = strconv.Itoa(c.Restarts)
	}
	if c.CPULimit > 0 {
		ch.Facts["CPU-Limit"] = strings.Replace(strconv.FormatFloat(c.CPULimit, 'f', -1, 64), ".", ",", 1) + " Kerne"
	}
	if c.HasStats && c.MemLimit > 0 {
		ch.Facts["RAM-Limit"] = fmtBytes(c.MemLimit)
	}
	ch.Metrics = []model.Metric{{Name: "docker.restarts", Instance: inst, Value: float64(c.Restarts1h)}}
	ended := func() string {
		s := fmt.Sprintf("beendet mit Code %d", c.ExitCode)
		if c.OOM {
			s = "vom Kernel beendet – Arbeitsspeicher voll (OOM)"
		}
		if c.Finished > 0 {
			s += " vor " + fmtDuration(max(0, now.Sub(time.Unix(c.Finished, 0))))
		}
		return s
	}
	switch c.State {
	case "running":
		ch.Message = "läuft"
		if c.Started > 0 {
			ch.Message += " seit " + fmtDuration(max(0, now.Sub(time.Unix(c.Started, 0))))
		}
		switch c.Health {
		case "healthy":
			ch.Message += " (gesund)"
		case "starting":
			ch.Message += " (Healthcheck läuft an)"
		case "unhealthy":
			ch.State = model.StateCrit
			why := fmt.Sprintf("Healthcheck schlägt fehl (%d× in Folge)", c.Failing)
			if c.HealthOut != "" {
				why += ": " + c.HealthOut
			}
			ch.Message = why + " · " + ch.Message
		}
		if c.HasStats {
			ch.Message += fmt.Sprintf(" · CPU %.0f %% · RAM %s", c.CPU, fmtBytes(c.MemUsed))
			ch.Metrics = append(ch.Metrics,
				model.Metric{Name: "guest.cpu", Instance: inst, Value: c.CPU, Unit: "%"},
				model.Metric{Name: "guest.mem_used", Instance: inst, Value: c.MemUsed, Unit: "B"})
			if c.MemLimit > 0 {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: "guest.mem", Instance: inst, Value: round2(100 * c.MemUsed / c.MemLimit), Unit: "%"})
			}
		}
		if c.HasRates {
			ch.Metrics = append(ch.Metrics,
				model.Metric{Name: "guest.netin", Instance: inst, Value: c.NetIn, Unit: "B/s"},
				model.Metric{Name: "guest.netout", Instance: inst, Value: c.NetOut, Unit: "B/s"})
		}
		if c.Restarts1h > 0 && ch.State == model.StateOK {
			ch.Message += fmt.Sprintf(" · %d× neu gestartet in der letzten Stunde", c.Restarts1h)
		}
	case "restarting":
		ch.State = model.StateCrit
		ch.Message = "startet ständig neu – " + ended()
		if c.Restarts1h > 0 {
			ch.Message += fmt.Sprintf(" · %d Neustarts in der letzten Stunde", c.Restarts1h)
		}
	case "paused":
		ch.State = model.StateWarn
		ch.Message = "angehalten (pausiert)"
	case "created":
		ch.Message = "angelegt, nie gestartet"
	case "removing":
		ch.Message = "wird entfernt"
	default: // exited, dead
		switch {
		case c.mustRun() || c.OOM && c.Policy != "no":
			ch.State = model.StateCrit
			ch.Message = "läuft nicht – " + ended()
		case c.State == "dead":
			ch.State = model.StateWarn
			ch.Message = "defekt (dead) – lässt sich nicht entfernen oder starten"
		default:
			ch.Message = ended()
		}
	}
	if c.Error != "" && c.State != "running" {
		ch.Facts["Fehler"] = truncate(c.Error, 200)
	}
	return ch
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 || b == float64(int64(b)) {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", b, units[i]), ".", ",", 1)
}

func fmtDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tagen"
	case d >= 24*time.Hour:
		return "1 Tag"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	}
	return strconv.Itoa(max(1, int(d.Minutes()))) + " Min."
}
