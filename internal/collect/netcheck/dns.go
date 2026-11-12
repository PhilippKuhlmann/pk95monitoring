package netcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

// maxAnswersShown limits the answers listed in the message.
const maxAnswersShown = 5

// newResolver returns the resolver for a dns check: the system resolver, or a pure Go
// resolver that sends every query to server (host:port).
var newResolver = func(server string) *net.Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server)
		},
	}
}

// runDNS resolves the target and optionally compares the answers with c.Expect.
func runDNS(ctx context.Context, res *model.Check, c model.NetCheck, timeout time.Duration) {
	server := ""
	if s := strings.TrimSpace(c.Server); s != "" {
		server, _ = endpoint(s, "", "53")
	}
	name, typ := trimDot(strings.TrimSpace(c.Target)), recordType(c)
	start := time.Now()
	// The trailing dot makes the name absolute: no search domains from resolv.conf are
	// tried (with a wildcard record in a search domain they would give a wrong answer).
	answers, err := lookup(ctx, newResolver(server), name+".", typ)
	elapsed := time.Since(start)
	if err != nil {
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			// Go reports NXDOMAIN and "name exists, no record of this type" alike.
			res.State, res.Message = model.StateCrit, fmt.Sprintf("Kein %s-Eintrag für %s (Name unbekannt oder ohne %s-Eintrag)", typ, name, typ)
		case errors.As(err, &dnsErr) && dnsErr.IsTimeout || errors.Is(err, context.DeadlineExceeded):
			res.State, res.Message = model.StateCrit, errText(context.DeadlineExceeded, timeout)
		case errors.As(err, &dnsErr):
			res.State, res.Message = model.StateCrit, "DNS-Fehler: "+dnsErr.Err
		default:
			res.State, res.Message = model.StateCrit, errText(err, timeout)
		}
		return
	}
	res.Metrics = append(res.Metrics, model.Metric{Name: "dns.time", Value: ms(elapsed), Unit: "ms"})
	if len(answers) == 0 {
		res.State, res.Message = model.StateCrit, fmt.Sprintf("Kein %s-Eintrag für %s", typ, name)
		return
	}
	shown := answerList(answers)
	if c.Expect != "" && !matchAny(answers, c.Expect) {
		res.State, res.Message = model.StateCrit, fmt.Sprintf("Erwartet %s, erhalten %s", c.Expect, shown)
		return
	}
	res.State, res.Message = model.StateOK, fmt.Sprintf("%s: %s (%s)", typ, shown, fmtMs(elapsed))
}

// lookup queries one record type. MX answers are "host" (the preference is dropped, so
// Expect can name the mail server).
func lookup(ctx context.Context, r *net.Resolver, name, typ string) ([]string, error) {
	var out []string
	switch typ {
	case "A", "AAAA":
		network := "ip4"
		if typ == "AAAA" {
			network = "ip6"
		}
		ips, err := r.LookupIP(ctx, network, name)
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		return out, err
	case "CNAME":
		cname, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		// LookupCNAME returns the name itself when it has address records but no CNAME.
		if strings.EqualFold(trimDot(cname), trimDot(name)) {
			return nil, nil
		}
		return []string{trimDot(cname)}, nil
	case "MX":
		mxs, err := r.LookupMX(ctx, name)
		for _, mx := range mxs {
			out = append(out, trimDot(mx.Host))
		}
		return out, err
	case "TXT":
		return r.LookupTXT(ctx, name)
	case "NS":
		nss, err := r.LookupNS(ctx, name)
		for _, ns := range nss {
			out = append(out, trimDot(ns.Host))
		}
		return out, err
	}
	return nil, errors.New("Unbekannter Eintragstyp " + strconv.Quote(typ))
}

// matchAny reports whether one answer equals want (case-insensitive, trailing dot and IP
// notation ignored).
func matchAny(answers []string, want string) bool {
	want = trimDot(strings.TrimSpace(want))
	wantIP := net.ParseIP(want)
	for _, a := range answers {
		a = trimDot(a)
		if strings.EqualFold(a, want) {
			return true
		}
		if ip := net.ParseIP(a); ip != nil && wantIP != nil && ip.Equal(wantIP) {
			return true
		}
	}
	return false
}

func answerList(answers []string) string {
	if len(answers) <= maxAnswersShown {
		return strings.Join(answers, ", ")
	}
	return strings.Join(answers[:maxAnswersShown], ", ") + fmt.Sprintf(" (+%d weitere)", len(answers)-maxAnswersShown)
}

func trimDot(s string) string { return strings.TrimSuffix(s, ".") }
