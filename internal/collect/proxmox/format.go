package proxmox

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fmtBytes renders a byte count with binary units and a German decimal comma ("1,5 TiB").
func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", b, units[i]), ".", ",", 1)
}

// fmtPct renders a percentage without decimals ("45 %").
func fmtPct(p float64) string { return strconv.Itoa(int(p+0.5)) + " %" }

// fmtFloat renders a number with two decimals and a German comma ("0,52").
func fmtFloat(f float64) string { return strings.Replace(fmt.Sprintf("%.2f", f), ".", ",", 1) }

// fmtDuration renders an uptime in German ("12 Tage", "5 Std.", "3 Min.").
func fmtDuration(sec float64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " Tage"
	case d >= 24*time.Hour:
		return "1 Tag"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + " Std."
	default:
		return strconv.Itoa(int(d.Minutes())) + " Min."
	}
}

// fmtSince renders an uptime after "seit" (dative: "seit 12 Tagen").
func fmtSince(sec float64) string {
	s := fmtDuration(sec)
	if strings.HasSuffix(s, " Tage") {
		s += "n"
	}
	return "seit " + s
}

// fmtTime renders a unix time in local time ("11.11.2026 01:05").
func fmtTime(unix float64) string { return time.Unix(int64(unix), 0).Format("02.01.2006 15:04") }

func pct(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return used / total * 100
}

// round2 keeps metric values short in JSON and the database.
func round2(v float64) float64 {
	if v >= 1e6 || v <= -1e6 {
		return float64(int64(v))
	}
	return float64(int64(v*100+sign(v)*0.5)) / 100
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
