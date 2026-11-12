package nut

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultPort is the TCP port of upsd.
const DefaultPort = 3493

// DefaultTimeout limits one poll or test (connect, login and all queries) when the
// context has no earlier deadline.
const DefaultTimeout = 10 * time.Second

const (
	maxLine  = 8192  // bytes per response line
	maxLines = 10000 // lines per LIST answer
	maxUPS   = 64    // UPS per server
)

// respError is an "ERR <code>" answer of upsd (RFC 9271, section "Error responses").
type respError struct{ code string }

func (e *respError) Error() string {
	return "NUT-Server: " + codeText(e.code) + " (" + e.code + ")"
}

// errCode returns the NUT error code of err, or "" when err is no ERR answer.
func errCode(err error) string {
	var re *respError
	if errors.As(err, &re) {
		return re.code
	}
	return ""
}

// codeText translates a NUT error code into German.
func codeText(code string) string {
	switch code {
	case "ACCESS-DENIED":
		return "Zugriff verweigert"
	case "UNKNOWN-UPS":
		return "USV auf dem Server nicht bekannt"
	case "VAR-NOT-SUPPORTED":
		return "Variable nicht unterstützt"
	case "DATA-STALE":
		return "Treiber liefert keine aktuellen Daten"
	case "DRIVER-NOT-CONNECTED":
		return "USV-Treiber nicht verbunden"
	case "INVALID-USERNAME":
		return "Ungültiger Benutzername"
	case "INVALID-PASSWORD":
		return "Ungültiges Passwort"
	case "USERNAME-REQUIRED":
		return "Benutzername erforderlich"
	case "PASSWORD-REQUIRED":
		return "Passwort erforderlich"
	case "ALREADY-SET-USERNAME", "ALREADY-SET-PASSWORD":
		return "Anmeldedaten bereits gesetzt"
	case "UNKNOWN-COMMAND":
		return "Befehl nicht unterstützt"
	case "INVALID-ARGUMENT":
		return "Ungültiges Argument"
	case "FEATURE-NOT-SUPPORTED", "FEATURE-NOT-CONFIGURED":
		return "Funktion nicht verfügbar"
	}
	return "Fehler"
}

// client is one connection to upsd. It is not safe for concurrent use.
type client struct {
	conn    net.Conn
	r       *bufio.Reader
	addr    string
	ctx     context.Context
	timeout time.Duration
	stop    func() bool
}

// dial connects to host:port. All later reads and writes share one deadline: the
// earlier of ctx's deadline and now+timeout. Cancelling ctx closes the connection.
func dial(ctx context.Context, host string, port int, timeout time.Duration) (*client, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("NUT: kein Host angegeben")
	}
	if port == 0 {
		port = DefaultPort
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("NUT: ungültiger Port %d", port)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
		timeout = max(time.Until(d), 0)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", addr)
	c := &client{addr: addr, ctx: ctx, timeout: timeout}
	if err != nil {
		return nil, c.netErr(err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, c.netErr(err)
	}
	c.conn, c.r = conn, bufio.NewReaderSize(conn, maxLine)
	c.stop = context.AfterFunc(ctx, func() { conn.Close() })
	return c, nil
}

// close says LOGOUT (best effort) and closes the connection.
func (c *client) close() {
	if c.conn == nil {
		return
	}
	if c.ctx.Err() == nil {
		_ = c.conn.SetDeadline(time.Now().Add(time.Second))
		if c.send("LOGOUT") == nil {
			_, _ = c.readLine()
		}
	}
	c.stop()
	c.conn.Close()
	c.conn = nil
}

// netErr turns a connection error into a German message.
func (c *client) netErr(err error) error {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case c.ctx.Err() != nil && errors.Is(c.ctx.Err(), context.Canceled):
		return errors.New("NUT: Abfrage abgebrochen")
	case errors.Is(err, context.DeadlineExceeded) || c.ctx.Err() != nil ||
		errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Errorf("NUT-Server %s antwortet nicht (Zeitüberschreitung nach %s)", c.addr, fmtSeconds(c.timeout))
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return fmt.Errorf("NUT: Name nicht auflösbar: %s", dnsErr.Name)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("NUT-Server %s: Verbindung abgelehnt (läuft upsd und lauscht er auf dieser Adresse?)", c.addr)
	case errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed):
		return fmt.Errorf("NUT-Server %s hat die Verbindung geschlossen", c.addr)
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH):
		return fmt.Errorf("NUT-Server %s nicht erreichbar (keine Route zum Host)", c.addr)
	}
	return fmt.Errorf("NUT-Server %s nicht erreichbar: %v", c.addr, err)
}

// send writes one command line.
func (c *client) send(line string) error {
	if _, err := io.WriteString(c.conn, line+"\n"); err != nil {
		return c.netErr(err)
	}
	return nil
}

// readLine reads one response line and splits it into words. An "ERR" line becomes a
// *respError.
func (c *client) readLine() ([]string, error) {
	b, err := c.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("NUT-Server %s: Antwortzeile länger als %d Byte", c.addr, maxLine)
	}
	if err != nil {
		return nil, c.netErr(err)
	}
	w, err := parseLine(strings.TrimRight(string(b), "\r\n"))
	if err != nil {
		return nil, fmt.Errorf("NUT-Server %s: ungültige Antwort: %w", c.addr, err)
	}
	if len(w) > 0 && w[0] == "ERR" {
		code := "UNKNOWN"
		if len(w) > 1 {
			code = w[1]
		}
		return nil, &respError{code: code}
	}
	return w, nil
}

// raw sends a command and returns the reply line unparsed (used for VER).
func (c *client) raw(cmd string) (string, error) {
	if err := c.send(cmd); err != nil {
		return "", err
	}
	b, err := c.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", fmt.Errorf("NUT-Server %s: Antwortzeile länger als %d Byte", c.addr, maxLine)
	}
	if err != nil {
		return "", c.netErr(err)
	}
	line := strings.TrimRight(string(b), "\r\n")
	if code, ok := strings.CutPrefix(line, "ERR "); ok {
		code, _, _ = strings.Cut(code, " ")
		return "", &respError{code: code}
	}
	return line, nil
}

// simple sends a command that is answered with "OK …".
func (c *client) simple(cmd string) error {
	if err := c.send(cmd); err != nil {
		return err
	}
	w, err := c.readLine()
	if err != nil {
		return err
	}
	if len(w) == 0 || w[0] != "OK" {
		return fmt.Errorf("NUT-Server %s: unerwartete Antwort auf %s", c.addr, strings.Fields(cmd)[0])
	}
	return nil
}

// login sends USERNAME and PASSWORD (when set). A refused login is an error.
func (c *client) login(user, pass string) error {
	if user == "" && pass == "" {
		return nil
	}
	if user != "" {
		q, err := quote(user)
		if err != nil {
			return fmt.Errorf("NUT: Benutzername %w", err)
		}
		if err := c.simple("USERNAME " + q); err != nil {
			return loginErr(err)
		}
	}
	if pass != "" {
		q, err := quote(pass)
		if err != nil {
			return fmt.Errorf("NUT: Passwort %w", err)
		}
		if err := c.simple("PASSWORD " + q); err != nil {
			return loginErr(err)
		}
	}
	return nil
}

func loginErr(err error) error {
	switch errCode(err) {
	case "ACCESS-DENIED", "INVALID-USERNAME", "INVALID-PASSWORD":
		return errors.New("NUT-Server: Anmeldung abgelehnt – Benutzername und Passwort prüfen")
	}
	return err
}

// list runs "LIST <what> <args…>" and returns the lines between BEGIN and END, each
// without the leading "<what> <args…>" words.
func (c *client) list(what string, args ...string) ([][]string, error) {
	head := []string{what}
	cmd := "LIST " + what
	for _, a := range args {
		q, err := quote(a)
		if err != nil {
			return nil, fmt.Errorf("NUT: %q %w", a, err)
		}
		cmd += " " + q
		head = append(head, a)
	}
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	w, err := c.readLine()
	if err != nil {
		return nil, err
	}
	if !hasWords(w, append([]string{"BEGIN", "LIST"}, head...)) || len(w) != len(head)+2 {
		return nil, fmt.Errorf("NUT-Server %s: unerwartete Antwort auf %s", c.addr, cmd)
	}
	// Entries repeat the list type (e.g. "VAR"), followed by the arguments.
	var out [][]string
	for range maxLines + 1 {
		w, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if hasWords(w, append([]string{"END", "LIST"}, head...)) {
			return out, nil
		}
		if !hasWords(w, head) {
			return nil, fmt.Errorf("NUT-Server %s: unerwartete Zeile in der Antwort auf %s", c.addr, cmd)
		}
		if len(out) == maxLines {
			break
		}
		out = append(out, w[len(head):])
	}
	return nil, fmt.Errorf("NUT-Server %s: mehr als %d Zeilen in der Antwort auf %s", c.addr, maxLines, cmd)
}

func hasWords(w, prefix []string) bool {
	if len(w) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if w[i] != p {
			return false
		}
	}
	return true
}

// upsEntry is one line of LIST UPS.
type upsEntry struct{ name, desc string }

// listUPS returns the UPS the server knows (at most maxUPS).
func (c *client) listUPS() ([]upsEntry, error) {
	lines, err := c.list("UPS")
	if err != nil {
		return nil, err
	}
	var out []upsEntry
	for _, l := range lines {
		if len(l) < 1 {
			continue
		}
		e := upsEntry{name: l[0]}
		if len(l) > 1 && l[1] != "Description unavailable" && l[1] != "Unavailable" {
			e.desc = strings.TrimSpace(l[1])
		}
		out = append(out, e)
		if len(out) == maxUPS {
			break
		}
	}
	return out, nil
}

// vars returns all variables of one UPS.
func (c *client) vars(ups string) (map[string]string, error) {
	lines, err := c.list("VAR", ups)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(lines))
	for _, l := range lines {
		if len(l) >= 2 {
			out[l[0]] = l[1]
		}
	}
	return out, nil
}

// parseLine splits a protocol line into words. Words are separated by spaces or tabs;
// a word in double quotes may contain spaces, and a backslash escapes the next
// character (\" and \\).
func parseLine(s string) ([]string, error) {
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
	if quoted {
		return nil, errors.New("Anführungszeichen nicht geschlossen")
	}
	if escaped {
		return nil, errors.New("Zeile endet mit einem Backslash")
	}
	if inWord {
		words = append(words, b.String())
	}
	return words, nil
}

// quote returns s as one protocol word: unchanged when it is plain, else in double
// quotes with " and \ escaped. Line breaks and NUL bytes are rejected, since they would
// end or corrupt the command.
func quote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("darf keinen Zeilenumbruch enthalten")
	}
	if s != "" && !strings.ContainsAny(s, " \t\"\\") {
		return s, nil
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`, nil
}

func fmtSeconds(d time.Duration) string {
	return fmtNum(d.Seconds(), 1) + " s"
}
