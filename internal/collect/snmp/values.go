package snmp

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// trimDot removes the leading dot gosnmp puts in front of OIDs.
func trimDot(oid string) string { return strings.TrimPrefix(strings.TrimSpace(oid), ".") }

// validOID reports whether s is a numeric OID such as "1.3.6.1.2.1.1.3.0".
func validOID(s string) bool {
	s = trimDot(s)
	if s == "" {
		return false
	}
	for part := range strings.SplitSeq(s, ".") {
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// CompareOID orders two numeric OIDs lexicographically by their sub-identifiers
// ("1.3.6.1.2" < "1.3.6.1.10"). A leading dot is ignored.
func CompareOID(a, b string) int {
	a, b = trimDot(a), trimDot(b)
	for a != "" && b != "" {
		var pa, pb string
		pa, a, _ = strings.Cut(a, ".")
		pb, b, _ = strings.Cut(b, ".")
		na, ea := strconv.ParseUint(pa, 10, 64)
		nb, eb := strconv.ParseUint(pb, 10, 64)
		if ea != nil || eb != nil {
			if c := strings.Compare(pa, pb); c != 0 {
				return c
			}
			continue
		}
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

// missing reports whether a PDU carries no value (unknown object, end of MIB).
func missing(p gosnmp.SnmpPDU) bool {
	switch p.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return true
	}
	return p.Value == nil
}

// toFloat converts a numeric PDU (or a numeric string) to float64.
func toFloat(p gosnmp.SnmpPDU) (float64, bool) {
	if missing(p) {
		return 0, false
	}
	switch v := p.Value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case []byte:
		return parseNum(cleanString(v))
	case string:
		if p.Type == gosnmp.OctetString {
			return parseNum(v)
		}
	}
	return 0, false
}

func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(strings.Replace(s, ",", ".", 1))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// toUint converts a counter or gauge PDU to uint64 (negative values are rejected).
func toUint(p gosnmp.SnmpPDU) (uint64, bool) {
	if missing(p) {
		return 0, false
	}
	switch v := p.Value.(type) {
	case uint:
		return uint64(v), true
	case uint32:
		return uint64(v), true
	case uint64:
		return v, true
	case int:
		if v >= 0 {
			return uint64(v), true
		}
	case int64:
		if v >= 0 {
			return uint64(v), true
		}
	}
	return 0, false
}

// toInt converts an INTEGER PDU (enums, levels) to int.
func toInt(p gosnmp.SnmpPDU) (int, bool) {
	f, ok := toFloat(p)
	if !ok || f < math.MinInt32 || f > math.MaxUint32 {
		return 0, false
	}
	return int(f), true
}

// toString renders a PDU as text: octet strings are cleaned, OIDs lose their leading dot.
func toString(p gosnmp.SnmpPDU) string {
	if missing(p) {
		return ""
	}
	switch v := p.Value.(type) {
	case []byte:
		return cleanString(v)
	case string:
		if p.Type == gosnmp.ObjectIdentifier {
			return trimDot(v)
		}
		return cleanString([]byte(v))
	}
	if f, ok := toFloat(p); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

// cleanString turns an octet string into readable text: trailing NULs and control
// characters are removed and invalid UTF-8 is read as Latin-1 (common on printers).
func cleanString(b []byte) string {
	var s string
	if utf8.Valid(b) {
		s = string(b)
	} else {
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		s = string(r)
	}
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// vals is the result of a GET keyed by OID without leading dot; missing objects are absent.
type vals map[string]gosnmp.SnmpPDU

func get(s Session, oids ...string) (vals, error) {
	pdus, err := s.Get(oids)
	if err != nil {
		return nil, err
	}
	v := make(vals, len(pdus))
	for _, p := range pdus {
		if !missing(p) {
			v[trimDot(p.Name)] = p
		}
	}
	return v, nil
}

func (v vals) str(oid string) string { return toString(v[oid]) }

func (v vals) num(oid string) (float64, bool) {
	p, ok := v[oid]
	if !ok {
		return 0, false
	}
	return toFloat(p)
}

func (v vals) int(oid string) (int, bool) {
	p, ok := v[oid]
	if !ok {
		return 0, false
	}
	return toInt(p)
}

// table holds columns of an SNMP table by column number and row index (OID suffix).
type table struct {
	rows []string // row indexes in OID order
	cols map[int]map[string]gosnmp.SnmpPDU
}

// walkTable walks the given columns of a table entry (e.g. "1.3.6.1.2.1.2.2.1"). The rows
// are those of the first column; when it is empty the other columns are not walked
// (missing MIB), which saves round trips.
func walkTable(s Session, entry string, cols ...int) (table, error) {
	t := table{cols: map[int]map[string]gosnmp.SnmpPDU{}}
	for i, col := range cols {
		root := entry + "." + strconv.Itoa(col)
		pdus, err := s.Walk(root)
		if err != nil {
			return table{}, err
		}
		m := make(map[string]gosnmp.SnmpPDU, len(pdus))
		for _, p := range pdus {
			idx, ok := strings.CutPrefix(trimDot(p.Name), root+".")
			if !ok || missing(p) {
				continue
			}
			if _, dup := m[idx]; !dup && i == 0 {
				t.rows = append(t.rows, idx)
			}
			m[idx] = p
		}
		t.cols[col] = m
		if i == 0 && len(t.rows) == 0 {
			break
		}
	}
	return t, nil
}

func (t table) has(col int, row string) bool {
	_, ok := t.cols[col][row]
	return ok
}

func (t table) str(col int, row string) string { return toString(t.cols[col][row]) }

func (t table) num(col int, row string) (float64, bool) {
	p, ok := t.cols[col][row]
	if !ok {
		return 0, false
	}
	return toFloat(p)
}

func (t table) int(col int, row string) (int, bool) {
	p, ok := t.cols[col][row]
	if !ok {
		return 0, false
	}
	return toInt(p)
}

func (t table) uint(col int, row string) (uint64, bool) {
	p, ok := t.cols[col][row]
	if !ok {
		return 0, false
	}
	return toUint(p)
}

// ---- formatting (German) ----

// fmtNum formats v with at most prec decimals and a decimal comma ("2,5"; "3").
func fmtNum(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return strings.Replace(s, ".", ",", 1)
}

func fmtPct(v float64) string { return fmtNum(math.Round(v), 0) + " %" }

// fmtBytes formats a byte count with binary units ("3,2 GiB").
func fmtBytes(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmtNum(b, 0) + " B"
	}
	return fmtNum(b, 1) + " " + units[i]
}

// fmtSpeed formats a link speed in bit/s ("1 Gbit/s", "2,5 Gbit/s").
func fmtSpeed(bps uint64) string {
	v := float64(bps)
	switch {
	case v >= 1e9:
		return fmtNum(v/1e9, 1) + " Gbit/s"
	case v >= 1e6:
		return fmtNum(v/1e6, 1) + " Mbit/s"
	case v >= 1e3:
		return fmtNum(v/1e3, 1) + " kbit/s"
	}
	return fmtNum(v, 0) + " bit/s"
}

// fmtUptime formats seconds as "12 Tage 3 Std." / "4 Std. 10 Min." / "7 Min.".
func fmtUptime(sec float64) string {
	s := int64(sec)
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d == 1:
		return "1 Tag " + strconv.FormatInt(h, 10) + " Std."
	case d > 1:
		return strconv.FormatInt(d, 10) + " Tage " + strconv.FormatInt(h, 10) + " Std."
	case h > 0:
		return strconv.FormatInt(h, 10) + " Std. " + strconv.FormatInt(m, 10) + " Min."
	}
	return strconv.FormatInt(m, 10) + " Min."
}
