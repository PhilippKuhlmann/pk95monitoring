package netcheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"

	"pk95monitoring/internal/model"
)

// runTCP checks that a TCP port accepts connections.
func runTCP(ctx context.Context, res *model.Check, addr string, timeout time.Duration) {
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)
	if err != nil {
		res.State, res.Message = model.StateCrit, errText(err, timeout)
		return
	}
	conn.Close()
	res.Metrics = append(res.Metrics, model.Metric{Name: "tcp.time", Value: ms(elapsed), Unit: "ms"})
	res.State, res.Message = model.StateOK, "Port offen ("+fmtMs(elapsed)+")"
}

// runTLS connects to addr and checks the server certificate: chain, host name and expiry.
// The certificate is read even when it is invalid, so cert.days and the facts are always
// reported.
func runTLS(ctx context.Context, res *model.Check, addr string, timeout time.Duration) {
	host, _, _ := net.SplitHostPort(addr)
	d := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		res.State, res.Message = model.StateCrit, errText(err, timeout)
		return
	}
	state := conn.(*tls.Conn).ConnectionState()
	conn.Close()
	if len(state.PeerCertificates) == 0 {
		res.State, res.Message = model.StateCrit, "Server hat kein Zertifikat gesendet"
		return
	}
	leaf := state.PeerCertificates[0]
	addCert(res, leaf)
	res.Facts["Namen"] = certNames(leaf)

	now := time.Now()
	opts := x509.VerifyOptions{
		Roots:         rootCAs,
		DNSName:       host,
		CurrentTime:   now,
		Intermediates: x509.NewCertPool(),
	}
	for _, c := range state.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(opts); err != nil {
		res.State, res.Message = model.StateCrit, "Zertifikat ungültig: "+certErrText(err)
		return
	}
	days := certDays(leaf, now)
	res.State = model.StateOK
	res.Message = fmt.Sprintf("Gültig bis %s (noch %.0f Tage)", leaf.NotAfter.Local().Format("02.01.2006"), days)
}
