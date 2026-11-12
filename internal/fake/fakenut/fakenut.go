// Package fakenut simulates a NUT server (upsd, RFC 9271) on 127.0.0.1 for tests and the
// demo mode. It answers VER, NETVER, USERNAME, PASSWORD, LIST UPS, LIST VAR, GET UPSDESC,
// GET VAR and LOGOUT; other commands get "ERR UNKNOWN-COMMAND".
package fakenut

import (
	"bufio"
	"errors"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is the answer to VER.
const Version = "Network UPS Tools upsd 2.8.2 - https://www.networkupstools.org/"

// Server is a running fake upsd. All methods are safe for concurrent use.
type Server struct {
	ln net.Listener

	mu       sync.Mutex
	upses    map[string]*ups
	user     string
	pass     string
	login    bool // RequireLogin was called
	drift    bool
	start    time.Time
	clock    func() time.Time
	conns    map[net.Conn]struct{}
	closed   bool
	requests int

	wg sync.WaitGroup
}

type ups struct {
	desc string
	vars map[string]string
	err  string // answer every LIST VAR / GET VAR with ERR <err>
}

// New starts a server on 127.0.0.1:0 with the given UPS (name → variables). It panics
// when it cannot listen, like httptest.NewServer.
func New(upses map[string]map[string]string) *Server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("fakenut: listen: " + err.Error())
	}
	s := &Server{ln: ln, upses: map[string]*ups{}, clock: time.Now, conns: map[net.Conn]struct{}{}}
	s.start = s.clock()
	for name, vars := range upses {
		s.upses[name] = &ups{desc: "Description unavailable", vars: clone(vars)}
	}
	s.wg.Add(1)
	go s.serve()
	return s
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Addr returns "127.0.0.1:<port>".
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Host returns the host to connect to ("127.0.0.1").
func (s *Server) Host() string { return s.ln.Addr().(*net.TCPAddr).IP.String() }

// Port returns the TCP port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops the server and closes all open connections.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// SetVar sets (or with an empty value removes) a variable; an unknown UPS is created.
func (s *Server) SetVar(upsName, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.get(upsName)
	if value == "" {
		delete(u.vars, name)
		return
	}
	u.vars[name] = value
}

// SetDesc sets the description LIST UPS reports (default "Description unavailable", as
// upsd says when ups.conf has no desc).
func (s *Server) SetDesc(upsName, desc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(upsName).desc = desc
}

// SetError makes LIST VAR and GET VAR of the UPS answer "ERR <code>" (e.g. "DATA-STALE",
// "DRIVER-NOT-CONNECTED"); an empty code restores normal answers.
func (s *Server) SetError(upsName, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(upsName).err = code
}

// Remove deletes a UPS.
func (s *Server) Remove(upsName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.upses, upsName)
}

// RequireLogin makes every LIST and GET need USERNAME user and PASSWORD pass first; a
// wrong user or password is answered with "ERR ACCESS-DENIED".
func (s *Server) RequireLogin(user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user, s.pass, s.login = user, pass, true
}

// EnableDrift lets load, voltages, runtime and temperatures of every UPS on mains power
// move slowly with time (sine waves of a few minutes), so the demo shows live charts.
func (s *Server) EnableDrift() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drift = true
}

// SetClock replaces the clock used for drift (default time.Now).
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
	s.start = now()
}

// Requests returns the number of commands received so far.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) get(name string) *ups {
	u := s.upses[name]
	if u == nil {
		u = &ups{desc: "Description unavailable", vars: map[string]string{}}
		s.upses[name] = u
	}
	return u
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handle(c)
	}
}

// session is the per-connection login state.
type session struct {
	user, pass       string
	hasUser, hasPass bool
}

func (s *Server) handle(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()
	r := bufio.NewReaderSize(c, 4096)
	w := bufio.NewWriter(c)
	var st session
	for {
		_ = c.SetDeadline(time.Now().Add(time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		words, err := parse(strings.TrimRight(line, "\r\n"))
		var out []string
		if err != nil {
			out = []string{"ERR INVALID-ARGUMENT"}
		} else {
			var quit bool
			out, quit = s.answer(&st, words)
			if quit {
				w.WriteString("OK Goodbye\n")
				w.Flush()
				return
			}
		}
		for _, l := range out {
			w.WriteString(l + "\n")
		}
		if w.Flush() != nil {
			return
		}
	}
}

// answer returns the response lines for one command.
func (s *Server) answer(st *session, w []string) (lines []string, quit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if len(w) == 0 {
		return []string{"ERR UNKNOWN-COMMAND"}, false
	}
	cmd := strings.ToUpper(w[0])
	args := w[1:]
	authed := !s.login || (st.hasUser && st.hasPass && st.user == s.user && st.pass == s.pass)
	switch {
	case cmd == "LOGOUT":
		return nil, true
	case cmd == "VER" && len(args) == 0:
		return []string{Version}, false
	case cmd == "NETVER" && len(args) == 0:
		return []string{"1.3"}, false
	case cmd == "STARTTLS":
		return []string{"ERR FEATURE-NOT-CONFIGURED"}, false
	case cmd == "USERNAME" && len(args) == 1:
		if st.hasUser {
			return []string{"ERR ALREADY-SET-USERNAME"}, false
		}
		st.user, st.hasUser = args[0], true
		return []string{"OK"}, false
	case cmd == "PASSWORD" && len(args) == 1:
		if st.hasPass {
			return []string{"ERR ALREADY-SET-PASSWORD"}, false
		}
		st.pass, st.hasPass = args[0], true
		if s.login && (st.user != s.user || st.pass != s.pass) {
			return []string{"ERR ACCESS-DENIED"}, false
		}
		return []string{"OK"}, false
	case cmd == "LIST" || cmd == "GET":
		if !authed {
			return []string{"ERR ACCESS-DENIED"}, false
		}
		return s.query(cmd, args), false
	case cmd == "USERNAME" || cmd == "PASSWORD":
		return []string{"ERR INVALID-ARGUMENT"}, false
	}
	return []string{"ERR UNKNOWN-COMMAND"}, false
}

func (s *Server) query(cmd string, args []string) []string {
	if len(args) == 0 {
		return []string{"ERR INVALID-ARGUMENT"}
	}
	sub := strings.ToUpper(args[0])
	switch {
	case cmd == "LIST" && sub == "UPS" && len(args) == 1:
		out := []string{"BEGIN LIST UPS"}
		for _, n := range s.names() {
			out = append(out, "UPS "+n+" "+quote(s.upses[n].desc))
		}
		return append(out, "END LIST UPS")
	case cmd == "LIST" && sub == "VAR" && len(args) == 2:
		u, vars, errLine := s.read(args[1])
		if errLine != "" {
			return []string{errLine}
		}
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out := []string{"BEGIN LIST VAR " + u}
		for _, k := range keys {
			out = append(out, "VAR "+u+" "+k+" "+quote(vars[k]))
		}
		return append(out, "END LIST VAR "+u)
	case cmd == "GET" && sub == "UPSDESC" && len(args) == 2:
		u := s.upses[args[1]]
		if u == nil {
			return []string{"ERR UNKNOWN-UPS"}
		}
		return []string{"UPSDESC " + args[1] + " " + quote(u.desc)}
	case cmd == "GET" && sub == "VAR" && len(args) == 3:
		u, vars, errLine := s.read(args[1])
		if errLine != "" {
			return []string{errLine}
		}
		v, ok := vars[args[2]]
		if !ok {
			return []string{"ERR VAR-NOT-SUPPORTED"}
		}
		return []string{"VAR " + u + " " + args[2] + " " + quote(v)}
	case sub == "UPS" || sub == "VAR" || sub == "RW" || sub == "CMD" || sub == "ENUM" || sub == "RANGE" || sub == "CLIENT":
		if cmd == "LIST" && len(args) == 2 {
			if _, _, errLine := s.read(args[1]); errLine != "" {
				return []string{errLine}
			}
			return []string{"BEGIN LIST " + sub + " " + args[1], "END LIST " + sub + " " + args[1]}
		}
		return []string{"ERR INVALID-ARGUMENT"}
	}
	return []string{"ERR INVALID-ARGUMENT"}
}

// read returns the variables of a UPS (with drift applied), or an ERR line.
func (s *Server) read(name string) (string, map[string]string, string) {
	u := s.upses[name]
	if u == nil {
		return "", nil, "ERR UNKNOWN-UPS"
	}
	if u.err != "" {
		return "", nil, "ERR " + u.err
	}
	vars := clone(u.vars)
	if s.drift {
		s.applyDrift(name, vars)
	}
	return name, vars, ""
}

func (s *Server) names() []string {
	n := make([]string, 0, len(s.upses))
	for k := range s.upses {
		n = append(n, k)
	}
	slices.Sort(n)
	return n
}

// applyDrift moves the numeric values around their configured base.
func (s *Server) applyDrift(name string, vars map[string]string) {
	if !slices.Contains(strings.Fields(vars["ups.status"]), "OL") {
		return
	}
	t := s.clock().Sub(s.start).Seconds()
	var ph float64
	for _, c := range name {
		ph += float64(c)
	}
	wave := func(period float64) float64 { return math.Sin(2*math.Pi*t/period + ph) }
	scale := func(key string, f func(v float64) float64, prec int) {
		v, err := strconv.ParseFloat(vars[key], 64)
		if err != nil {
			return
		}
		vars[key] = strconv.FormatFloat(f(v), 'f', prec, 64)
	}
	l := 1 + 0.25*wave(900)
	scale("ups.load", func(v float64) float64 { return math.Max(0, math.Round(v*l)) }, 0)
	scale("ups.realpower", func(v float64) float64 { return math.Round(v * l) }, 0)
	scale("battery.runtime", func(v float64) float64 { return math.Round(v / l) }, 0)
	scale("input.voltage", func(v float64) float64 { return v + 2.5*wave(420) }, 1)
	scale("output.voltage", func(v float64) float64 { return v + 0.6*wave(510) }, 1)
	scale("ups.temperature", func(v float64) float64 { return v + 1.2*wave(1800) }, 1)
	scale("battery.temperature", func(v float64) float64 { return v + 0.8*wave(2100) }, 1)
	scale("battery.voltage", func(v float64) float64 { return v + 0.1*wave(700) }, 2)
}

// UniFiUPS returns the variables of a UniFi UPS 2U (Ubiquiti) on mains power with a
// charging battery, as its built-in NUT server reports them.
func UniFiUPS() map[string]string {
	return map[string]string{
		"battery.charge":          "100",
		"battery.charge.low":      "10",
		"battery.charge.warning":  "20",
		"battery.mfr.date":        "2024/03/12",
		"battery.runtime":         "2460",
		"battery.runtime.low":     "120",
		"battery.temperature":     "29.5",
		"battery.type":            "PbAc",
		"battery.voltage":         "27.20",
		"battery.voltage.nominal": "24",
		"device.mfr":              "Ubiquiti",
		"device.model":            "UPS 2U",
		"device.serial":           "F4E2C6A1B9D3",
		"device.type":             "ups",
		"driver.name":             "usbhid-ups",
		"driver.version":          "2.8.2",
		"input.frequency":         "50.0",
		"input.voltage":           "230.4",
		"input.voltage.nominal":   "230",
		"output.frequency":        "50.0",
		"output.voltage":          "230.0",
		"output.voltage.nominal":  "230",
		"ups.beeper.status":       "enabled",
		"ups.firmware":            "1.3.7",
		"ups.load":                "18",
		"ups.mfr":                 "Ubiquiti",
		"ups.model":               "UPS 2U",
		"ups.power.nominal":       "1500",
		"ups.realpower.nominal":   "1200",
		"ups.serial":              "F4E2C6A1B9D3",
		"ups.status":              "OL CHRG",
		"ups.temperature":         "33.0",
		"ups.test.result":         "Done and passed",
		"ups.type":                "line-interactive",
	}
}

// parse splits a command line into words (double quotes, backslash escapes).
func parse(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	inWord, quoted, escaped := false, false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			b.WriteByte(ch)
			escaped = false
		case ch == '\\':
			escaped, inWord = true, true
		case quoted:
			if ch == '"' {
				quoted = false
			} else {
				b.WriteByte(ch)
			}
		case ch == '"':
			quoted, inWord = true, true
		case ch == ' ' || ch == '\t':
			if inWord {
				words = append(words, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteByte(ch)
			inWord = true
		}
	}
	if quoted || escaped {
		return nil, errors.New("unterminated quote or escape")
	}
	if inWord {
		words = append(words, b.String())
	}
	return words, nil
}

// quote returns s in double quotes with " and \ escaped (as upsd sends values).
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
