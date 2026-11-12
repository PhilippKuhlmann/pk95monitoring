package agent

import (
	"fmt"
	"math"
	"strings"
)

// deNum formats v with the given decimals and a German decimal comma.
func deNum(v float64, decimals int) string {
	return strings.Replace(fmt.Sprintf("%.*f", decimals, v), ".", ",", 1)
}

// humanBytes formats a byte count, e.g. "12,3 GB" (binary units, common labels).
func humanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(b) >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 || b >= 100 {
		return deNum(b, 0) + " " + units[i]
	}
	return deNum(b, 1) + " " + units[i]
}

// humanRate formats bytes per second, e.g. "1,2 MB/s".
func humanRate(b float64) string { return humanBytes(b) + "/s" }

// humanDuration formats seconds as "3 Tage 4 Std.", "5 Std. 12 Min." or "7 Min.".
func humanDuration(sec uint64) string {
	d, h, m := sec/86400, sec%86400/3600, sec%3600/60
	switch {
	case d > 0:
		day := "Tage"
		if d == 1 {
			day = "Tag"
		}
		return fmt.Sprintf("%d %s %d Std.", d, day, h)
	case h > 0:
		return fmt.Sprintf("%d Std. %d Min.", h, m)
	}
	return fmt.Sprintf("%d Min.", m)
}

// pct formats a percentage, e.g. "45 %" or "4,5 %" below 10.
func pct(v float64) string {
	if v < 10 && v != 0 {
		return deNum(v, 1) + " %"
	}
	return deNum(v, 0) + " %"
}

// round2 keeps metric values compact in JSON.
func round2(v float64) float64 { return math.Round(v*100) / 100 }
