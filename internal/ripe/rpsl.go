// Package ripe reads the RIPE Database split files and the geofeeds that
// network operators reference from them, and turns both into region evidence
// for the regional layer. The RIPE NCC permitted this use (see SOURCES.md);
// no RIPE Database object or attribute is republished, only the derived
// per-range regions.
package ripe

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/jinkela1145/open-IPGeo/internal/iprange"
)

// Object is the part of an inetnum or inet6num object the layer uses.
type Object struct {
	R         iprange.Range
	Countries []string // upper-case values of the country attributes
	Netname   string
	Descr     []string
	// Geofeed is the HTTPS URL of the object's geofeed, from a "geofeed:"
	// attribute or a "remarks: Geofeed <url>" line; empty when there is none
	// or the references disagree.
	Geofeed string
}

// HasCountry reports whether one of the object's country attributes is cc.
func (o *Object) HasCountry(cc string) bool {
	for _, c := range o.Countries {
		if c == cc {
			return true
		}
	}
	return false
}

// maxDescr bounds the descr lines kept per object.
const maxDescr = 16

// Scan reads a RIPE split file (plain text or gzip) and calls fn for every
// inetnum and inet6num object with a valid range. The Object passed to fn is
// reused for the next object; fn must copy what it keeps.
func Scan(path string, fn func(*Object) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	}
	if err := ScanReader(r, fn); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ScanReader is Scan on an uncompressed stream.
func ScanReader(r io.Reader, fn func(*Object) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var (
		obj      Object
		inObject bool // inside an object (any class)
		wanted   bool // the object is an inetnum or inet6num
		valid    bool // its range parsed
		last     string
		feeds    []string // geofeed attribute values
		remFeeds []string // remarks: Geofeed values
	)
	reset := func() {
		obj.R = iprange.Range{}
		obj.Countries = obj.Countries[:0]
		obj.Netname = ""
		obj.Descr = obj.Descr[:0]
		obj.Geofeed = ""
		inObject, wanted, valid, last = false, false, false, ""
		feeds, remFeeds = feeds[:0], remFeeds[:0]
	}
	finish := func() error {
		defer reset()
		if !wanted || !valid {
			return nil
		}
		obj.Geofeed = pickGeofeed(feeds, remFeeds)
		return fn(&obj)
	}
	reset()
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			if err := finish(); err != nil {
				return err
			}
			continue
		}
		switch line[0] {
		case '%', '#':
			continue
		case ' ', '\t', '+':
			// Continuation of the previous attribute.
			if wanted && last == "descr" && len(obj.Descr) > 0 {
				v := strings.TrimSpace(latin1(bytes.TrimLeft(line, " \t+")))
				if v != "" {
					obj.Descr[len(obj.Descr)-1] += " " + v
				}
			}
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		// Lower-case the attribute name in place; the switch below then
		// compares without allocating.
		name := bytes.TrimSpace(line[:colon])
		for i, c := range name {
			if 'A' <= c && c <= 'Z' {
				name[i] = c + 'a' - 'A'
			}
		}
		if !inObject {
			inObject = true
			wanted = string(name) == "inetnum" || string(name) == "inet6num"
		}
		if !wanted {
			continue
		}
		raw := line[colon+1:]
		switch string(name) {
		case "inetnum", "inet6num":
			last = ""
			if obj.R.Start.IsValid() {
				continue // only the first primary key counts
			}
			is4 := string(name) == "inetnum"
			if r, err := iprange.Parse(stripComment(string(raw))); err == nil && r.Is4() == is4 {
				obj.R, valid = r, true
			}
		case "netname":
			last = ""
			if obj.Netname == "" {
				obj.Netname = stripComment(latin1(raw))
			}
		case "descr":
			last = "descr"
			if len(obj.Descr) < maxDescr {
				obj.Descr = append(obj.Descr, strings.TrimSpace(latin1(raw)))
			}
		case "country":
			last = ""
			if c := strings.ToUpper(stripComment(string(raw))); len(c) == 2 {
				obj.Countries = append(obj.Countries, c)
			}
		case "geofeed":
			last = ""
			if u := firstField(string(raw)); u != "" {
				feeds = append(feeds, u)
			}
		case "remarks":
			last = ""
			if v := bytes.TrimSpace(raw); len(v) > 8 && bytes.EqualFold(v[:7], []byte("geofeed")) && (v[7] == ' ' || v[7] == '\t') {
				if f := strings.Fields(string(v)); len(f) >= 2 {
					remFeeds = append(remFeeds, f[1])
				}
			}
		default:
			last = ""
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return finish()
}

// pickGeofeed applies RFC 9632: the geofeed attribute is preferred over a
// remarks reference; several different references make the object unusable.
func pickGeofeed(attr, remarks []string) string {
	refs := attr
	if len(refs) == 0 {
		refs = remarks
	}
	if len(refs) == 0 {
		return ""
	}
	for _, u := range refs[1:] {
		if u != refs[0] {
			return ""
		}
	}
	if !ValidGeofeedURL(refs[0]) {
		return ""
	}
	return refs[0]
}

// ValidGeofeedURL accepts absolute HTTPS URLs with a host name and no user
// information, as RFC 9632 requires HTTPS.
func ValidGeofeedURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return u.Hostname() != ""
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// stripComment removes an RPSL end-of-line comment.
func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// latin1 decodes ISO-8859-1, the character set of the RIPE Database.
func latin1(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}
