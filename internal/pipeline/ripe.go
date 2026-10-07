package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jinkela1145/open-IPGeo/internal/config"
	"github.com/jinkela1145/open-IPGeo/internal/fetch"
	"github.com/jinkela1145/open-IPGeo/internal/ripe"
	"github.com/jinkela1145/open-IPGeo/internal/sources"
)

// GeofeedSummary records the geofeeds fetched for the RIPE layer. It holds
// hashes of the URLs only: the URLs come from the RIPE Database and are not
// republished.
type GeofeedSummary struct {
	Referenced  int `json:"referenced"`
	Downloaded  int `json:"downloaded"`
	NotModified int `json:"not_modified"`
	Reused      int `json:"reused"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped,omitempty"`
	// Usable maps the cache name of every geofeed the build may read to the
	// SHA-256 of its content (inputs.json only).
	Usable map[string]string `json:"usable,omitempty"`
	SHA256 string            `json:"sha256"`
}

// Limits of the geofeed download.
const (
	MaxGeofeeds        = 5000
	geofeedConcurrency = 16
	geofeedMaxBytes    = 64 << 20
	geofeedBudget      = 15 * time.Minute
)

// geofeedName is the cache name of a geofeed URL.
func geofeedName(url string) string {
	sum := sha256.Sum256([]byte(url))
	return "gf-" + hex.EncodeToString(sum[:12])
}

func geofeedDir(cacheDir string) string { return filepath.Join(cacheDir, "geofeeds") }

// ripeTables are the region tables the RIPE layer covers.
func ripeTables() []config.RegionTable {
	var out []config.RegionTable
	for _, t := range config.RegionTables {
		if t.RIPE {
			out = append(out, t)
		}
	}
	return out
}

// refusePrivate stops geofeed downloads from reaching loopback, private,
// link-local and other non-public addresses.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	a = a.Unmap()
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	if !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) {
		return fmt.Errorf("refusing to connect to non-public address %s", a)
	}
	return nil
}

// geofeedClient is the HTTP client for geofeeds: HTTPS only (RFC 9632), no
// redirects to plain HTTP, public addresses only unless a proxy is set.
func geofeedClient() *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if os.Getenv("HTTPS_PROXY") == "" && os.Getenv("https_proxy") == "" {
		d.Control = refusePrivate
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   2,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to a non-HTTPS URL")
		}
		return nil
	}}
}

// fetchGeofeeds downloads the geofeeds referenced by RIPE objects of the
// covered countries. Failures fall back to a cached copy fetched within
// MaxStaleAge; they never fail the run.
func fetchGeofeeds(ctx context.Context, opt FetchOptions, splitFiles []string, ua string) (*GeofeedSummary, error) {
	var codes []string
	for _, t := range ripeTables() {
		codes = append(codes, t.Country)
	}
	urls, err := ripe.GeofeedURLs(splitFiles, codes)
	if err != nil {
		return nil, err
	}
	sum := &GeofeedSummary{Referenced: len(urls), Usable: map[string]string{}}
	if len(urls) > MaxGeofeeds {
		sum.Skipped = len(urls) - MaxGeofeeds
		urls = urls[:MaxGeofeeds]
	}
	client := opt.GeofeedClient
	if client == nil {
		client = geofeedClient()
	}
	gf := fetch.New(geofeedDir(opt.CacheDir), ua)
	gf.Client = client
	gf.Attempts, gf.Backoff, gf.Timeout, gf.MaxBytes = 2, 2*time.Second, time.Minute, geofeedMaxBytes
	gf.Now = func() time.Time { return opt.Now }
	// No per-URL logging: the URLs come from the RIPE Database.
	gf.Logf = func(string, ...any) {}

	ctx, cancel := context.WithTimeout(ctx, geofeedBudget)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, geofeedConcurrency)
	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			name := geofeedName(u)
			_, m, err := gf.Fetch(ctx, name, u)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && m.Cached:
				sum.NotModified++
			case err == nil:
				sum.Downloaded++
			default:
				_, cm, ok := gf.Cached(name)
				t, perr := time.Parse(time.RFC3339, cm.FetchedAt)
				if !ok || cm.URL != u || perr != nil || opt.Now.Sub(t) > MaxStaleAge {
					sum.Failed++
					return
				}
				m = cm
				sum.Reused++
			}
			sum.Usable[name] = m.SHA256
		}(u)
	}
	wg.Wait()
	names := make([]string, 0, len(sum.Usable))
	for n := range sum.Usable {
		names = append(names, n)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s %s\n", n, sum.Usable[n])
	}
	sum.SHA256 = hex.EncodeToString(h.Sum(nil))
	return sum, nil
}

// ripeSplitFiles returns the cached RIPE split files listed in inputs.json.
func ripeSplitFiles(cacheDir string, in *InputsFile) []string {
	var out []string
	for _, name := range config.RIPESources {
		if _, ok := in.Sources[name]; ok {
			out = append(out, filepath.Join(cacheDir, name+".data"))
		}
	}
	return out
}

// RIPEReport is the RIPE layer part of manifest.json.
type RIPEReport struct {
	Stats    *ripe.Stats     `json:"stats"`
	Geofeeds *GeofeedSummary `json:"geofeeds,omitempty"`
}

// loadRIPE builds the region hints of the RIPE layer. Problems with the RIPE
// files only turn the layer off for this build (with a warning).
func loadRIPE(opt BuildOptions, l *Loaded) {
	files := ripeSplitFiles(opt.CacheDir, opt.Inputs)
	tables := ripeTables()
	if len(files) == 0 || len(tables) == 0 {
		return
	}
	var countries []ripe.Country
	for _, t := range tables {
		c, err := ripeCountry(opt.DataDir, t, l.In.Regions)
		if err != nil {
			l.Warnings = append(l.Warnings, fmt.Sprintf("RIPE layer disabled: %v", err))
			return
		}
		countries = append(countries, c)
	}
	usable := map[string]string{}
	if g := opt.Inputs.Geofeeds; g != nil {
		usable = g.Usable
	}
	dir := geofeedDir(opt.CacheDir)
	hints, st, err := ripe.Collect(files, countries, func(u string) (string, bool) {
		name := geofeedName(u)
		if _, ok := usable[name]; !ok {
			return "", false
		}
		p := filepath.Join(dir, name+".data")
		if _, err := os.Stat(p); err != nil {
			return "", false
		}
		return p, true
	})
	if err != nil {
		l.Warnings = append(l.Warnings, fmt.Sprintf("RIPE layer disabled: %v", err))
		return
	}
	l.In.RegionHints = hints
	l.RIPE = &RIPEReport{Stats: st}
	if g := opt.Inputs.Geofeeds; g != nil {
		c := *g
		c.Usable = nil
		l.RIPE.Geofeeds = &c
	}
	l.Layers["ripe_objects"] = st.Objects
	l.Layers["ripe_geofeeds_read"] = st.GeofeedsRead
	for cc, n := range st.CountryObjects {
		l.Layers["ripe_objects_"+strings.ToLower(cc)] = n
	}
	for cc, n := range st.WhoisHints {
		l.Layers["ripe_whois_hints_"+strings.ToLower(cc)] = n
	}
	for cc, n := range st.Accepted {
		l.Layers["ripe_geofeed_entries_"+strings.ToLower(cc)] = n
	}
	opt.Logf("RIPE layer: %s", st)
}

// ripeCountry assembles the vocabulary of one covered country from its
// admin table (names, DB-IP spellings, capitals) and place-name table.
func ripeCountry(dataDir string, t config.RegionTable, all []sources.Region) (ripe.Country, error) {
	c := ripe.Country{Code: t.Country, Regions: map[string]bool{}}
	var names []ripe.Name
	for _, r := range all {
		if r.Country != t.Country {
			continue
		}
		c.Regions[r.ISO] = true
		names = append(names, ripe.Name{Text: r.NameEN, Region: r.ISO})
		for _, n := range r.DBIPNames {
			names = append(names, ripe.Name{Text: n, Region: r.ISO})
		}
		if r.CapitalEN != "" {
			names = append(names, ripe.Name{Text: r.CapitalEN, Region: r.ISO})
		}
	}
	if t.PlaceNamesFile != "" {
		extra, err := sources.ReadPlaceNames(filepath.Join(dataDir, t.PlaceNamesFile))
		if err != nil {
			return c, err
		}
		for _, p := range extra {
			if p.RegionISO != "" && !c.Regions[p.RegionISO] {
				return c, fmt.Errorf("%s: %q refers to unknown region %s", t.PlaceNamesFile, p.Name, p.RegionISO)
			}
			names = append(names, ripe.Name{Text: p.Name, Region: p.RegionISO})
		}
	}
	c.Vocab = ripe.NewVocab(names, t.CarrierHQ, t.HQNeighbors)
	return c, nil
}

// topWhoisHints returns the largest whois hints, for review.
func topWhoisHints(hints []ripe.Hint, n int) []ripe.Hint {
	var out []ripe.Hint
	for _, h := range hints {
		if h.Via == ripe.ViaWhois {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].R.Size().Float64(), out[j].R.Size().Float64()
		if out[i].R.Is4() != out[j].R.Is4() {
			return out[i].R.Is4()
		}
		return a > b
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
