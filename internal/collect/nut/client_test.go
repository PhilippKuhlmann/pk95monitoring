package nut

import (
	"bufio"
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		err  bool
	}{
		{`BEGIN LIST UPS`, []string{"BEGIN", "LIST", "UPS"}, false},
		{`UPS ups1 "Server room UPS"`, []string{"UPS", "ups1", "Server room UPS"}, false},
		{`VAR ups1 ups.mfr "say \"hi\" \\ ok"`, []string{"VAR", "ups1", "ups.mfr", `say "hi" \ ok`}, false},
		{`VAR ups1 x ""`, []string{"VAR", "ups1", "x", ""}, false},
		{"  a\t b  ", []string{"a", "b"}, false},
		{`a"b c"d`, []string{"ab cd"}, false},
		{`ERR ACCESS-DENIED`, []string{"ERR", "ACCESS-DENIED"}, false},
		{`VAR ups1 x "open`, nil, true},
		{`VAR ups1 x \`, nil, true},
		{``, nil, false},
	}
	for _, tt := range tests {
		got, err := parseLine(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("parseLine(%q) error = %v, want error %v", tt.in, err, tt.err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("parseLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"monuser", "monuser"},
		{"pass word", `"pass word"`},
		{`a"b\c`, `"a\"b\\c"`},
		{"", `""`},
	}
	for _, tt := range tests {
		got, err := quote(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("quote(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		back, err := parseLine(got)
		if err != nil || len(back) != 1 || back[0] != tt.in {
			t.Errorf("parseLine(quote(%q)) = %q, %v", tt.in, back, err)
		}
	}
	for _, bad := range []string{"a\nLIST UPS", "a\rb", "a\x00"} {
		if _, err := quote(bad); err == nil {
			t.Errorf("quote(%q) accepted a line break", bad)
		}
	}
}

// scripted starts a server that answers each received command with the next reply.
func scripted(t *testing.T, replies ...string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		for _, rep := range replies {
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			if _, err := c.Write([]byte(rep)); err != nil {
				return
			}
		}
		_, _ = r.ReadString('\n')
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func TestClientList(t *testing.T) {
	host, port := scripted(t,
		"BEGIN LIST VAR ups1\nVAR ups1 a \"1\"\nVAR ups1 b \"x \\\"y\\\"\"\r\nEND LIST VAR ups1\n")
	c, err := dial(context.Background(), host, port, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	v, err := c.vars("ups1")
	if err != nil {
		t.Fatal(err)
	}
	if v["a"] != "1" || v["b"] != `x "y"` || len(v) != 2 {
		t.Errorf("vars = %q", v)
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name, reply, want string
		code              string
	}{
		{"err", "ERR DATA-STALE\n", "", "DATA-STALE"},
		{"bad begin", "BEGIN LIST UPS\n", "unerwartete Antwort", ""},
		{"foreign line", "BEGIN LIST VAR ups1\nVAR other a \"1\"\n", "unerwartete Zeile", ""},
		{"long line", "BEGIN LIST VAR ups1\nVAR ups1 a \"" + strings.Repeat("x", maxLine) + "\"\n", "länger als", ""},
		{"bad quote", "BEGIN LIST VAR ups1\nVAR ups1 a \"open\n", "ungültige Antwort", ""},
		{"eof", "BEGIN LIST VAR ups1\n", "Verbindung geschlossen", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port := scripted(t, tt.reply)
			c, err := dial(context.Background(), host, port, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			if tt.name == "eof" {
				// The scripted server waits for one more line before it closes.
				go func() { time.Sleep(50 * time.Millisecond); c.send("NOOP") }()
			}
			_, err = c.vars("ups1")
			if err == nil {
				t.Fatal("no error")
			}
			if tt.code != "" && errCode(err) != tt.code {
				t.Errorf("code = %q, want %q (%v)", errCode(err), tt.code, err)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestClientTooManyLines(t *testing.T) {
	var b strings.Builder
	b.WriteString("BEGIN LIST VAR ups1\n")
	for range maxLines + 1 {
		b.WriteString("VAR ups1 a \"1\"\n")
	}
	b.WriteString("END LIST VAR ups1\n")
	host, port := scripted(t, b.String())
	c, err := dial(context.Background(), host, port, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if _, err := c.vars("ups1"); err == nil || !strings.Contains(err.Error(), "mehr als") {
		t.Errorf("error = %v", err)
	}
}

func TestClientTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(time.Second)
			c.Close()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	c, err := dial(context.Background(), a.IP.String(), a.Port, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	start := time.Now()
	_, err = c.listUPS()
	if err == nil || !strings.Contains(err.Error(), "Zeitüberschreitung nach 0,1 s") {
		t.Errorf("error = %v", err)
	}
	if time.Since(start) > 800*time.Millisecond {
		t.Errorf("deadline not applied: %v", time.Since(start))
	}
}

func TestClientCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			c.Close()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := dial(ctx, a.IP.String(), a.Port, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := c.listUPS(); err == nil || !strings.Contains(err.Error(), "abgebrochen") {
		t.Errorf("error = %v", err)
	}
}

func TestDialArgs(t *testing.T) {
	if _, err := dial(context.Background(), " ", 0, time.Second); err == nil {
		t.Error("empty host accepted")
	}
	if _, err := dial(context.Background(), "127.0.0.1", 70000, time.Second); err == nil {
		t.Error("bad port accepted")
	}
}
