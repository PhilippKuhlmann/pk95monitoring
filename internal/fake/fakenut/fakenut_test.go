package fakenut

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// talk sends commands over one connection and returns all answer lines.
func talk(t *testing.T, s *Server, cmds ...string) []string {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	var out []string
	for _, cmd := range cmds {
		if _, err := c.Write([]byte(cmd + "\n")); err != nil {
			t.Fatal(err)
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			line = strings.TrimRight(line, "\n")
			out = append(out, line)
			if !strings.HasPrefix(line, "BEGIN ") && !strings.HasPrefix(line, "VAR ") && !strings.HasPrefix(line, "UPS ") ||
				strings.HasPrefix(line, "VAR ") && strings.HasPrefix(cmd, "GET") {
				break
			}
		}
	}
	return out
}

func TestServer(t *testing.T) {
	s := New(map[string]map[string]string{"ups": {"ups.status": "OL", "x": `a "b" \c`}})
	defer s.Close()
	s.SetDesc("ups", "Rack USV")
	got := strings.Join(talk(t, s, "VER", "LIST UPS", "LIST VAR ups", "GET VAR ups x", "GET VAR ups y", "LIST VAR nope", "FOO", "LOGOUT"), "\n")
	want := strings.Join([]string{
		Version,
		"BEGIN LIST UPS", `UPS ups "Rack USV"`, "END LIST UPS",
		"BEGIN LIST VAR ups", `VAR ups ups.status "OL"`, `VAR ups x "a \"b\" \\c"`, "END LIST VAR ups",
		`VAR ups x "a \"b\" \\c"`,
		"ERR VAR-NOT-SUPPORTED",
		"ERR UNKNOWN-UPS",
		"ERR UNKNOWN-COMMAND",
		"OK Goodbye",
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if s.Requests() != 8 {
		t.Errorf("requests = %d", s.Requests())
	}
}

func TestServerLogin(t *testing.T) {
	s := New(map[string]map[string]string{"ups": UniFiUPS()})
	defer s.Close()
	s.RequireLogin("mon", "pw")
	got := talk(t, s, "LIST UPS", "USERNAME mon", "PASSWORD pw", "LIST UPS")
	if got[0] != "ERR ACCESS-DENIED" || got[1] != "OK" || got[2] != "OK" || got[3] != "BEGIN LIST UPS" {
		t.Errorf("got %q", got)
	}
	got = talk(t, s, "USERNAME mon", "PASSWORD \"bad\"", "LIST UPS")
	if got[1] != "ERR ACCESS-DENIED" || got[2] != "ERR ACCESS-DENIED" {
		t.Errorf("got %q", got)
	}
}

func TestServerError(t *testing.T) {
	s := New(map[string]map[string]string{"ups": UniFiUPS()})
	defer s.Close()
	s.SetError("ups", "DATA-STALE")
	if got := talk(t, s, "LIST VAR ups"); got[0] != "ERR DATA-STALE" {
		t.Errorf("got %q", got)
	}
	s.SetError("ups", "")
	if got := talk(t, s, "GET VAR ups ups.status"); got[0] != `VAR ups ups.status "OL CHRG"` {
		t.Errorf("got %q", got)
	}
}
