package ripe

import (
	"bufio"
	"bytes"
	"io"
	"net/netip"
	"strings"
)

// Entry is one line of a geofeed (RFC 8805): prefix, country, region, city.
type Entry struct {
	P       netip.Prefix
	Country string // ISO 3166-1 alpha-2, upper case; empty if not given
	Region  string // as written, upper case; normally ISO 3166-2 ("RU-SVE")
	City    string
}

// ParseGeofeed reads a geofeed. Lines that cannot be parsed are counted and
// skipped; at most limit entries are returned (0 means no limit).
func ParseGeofeed(r io.Reader, limit int) (entries []Entry, bad int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			first = false
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		e, ok := parseGeofeedLine(string(line))
		if !ok {
			bad++
			continue
		}
		if limit > 0 && len(entries) >= limit {
			bad++
			continue
		}
		entries = append(entries, e)
	}
	return entries, bad, sc.Err()
}

func parseGeofeedLine(line string) (Entry, bool) {
	fields := strings.Split(line, ",")
	for i, f := range fields {
		fields[i] = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), `"`))
	}
	var e Entry
	if strings.Contains(fields[0], "%") {
		return Entry{}, false // zones have no place in a geofeed
	}
	p, err := netip.ParsePrefix(fields[0])
	if err != nil {
		a, aerr := netip.ParseAddr(fields[0])
		if aerr != nil {
			return Entry{}, false
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	e.P = p.Masked()
	if len(fields) > 1 {
		c := strings.ToUpper(fields[1])
		if c != "" && (len(c) != 2 || !isAlpha(c)) {
			return Entry{}, false
		}
		e.Country = c
	}
	if len(fields) > 2 {
		e.Region = strings.ToUpper(fields[2])
	}
	if len(fields) > 3 {
		e.City = fields[3]
	}
	return e, true
}

func isAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
