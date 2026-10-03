// Package pipeline ties fetching, parsing, building and reporting together.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jinkela1145/enhanced-geoip/internal/config"
	"github.com/jinkela1145/enhanced-geoip/internal/fetch"
	"github.com/jinkela1145/enhanced-geoip/internal/sources"
)

// BuilderVersion changes whenever the output format or merge logic changes,
// so that a new release is produced even if no upstream file changed.
const BuilderVersion = "2"

// InputsFile is written by Fetch and read by Build.
type InputsFile struct {
	BuilderVersion string                `json:"builder_version"`
	Fingerprint    string                `json:"fingerprint"`
	DBIPMonth      string                `json:"dbip_month"`
	ASNSource      string                `json:"asn_source"` // iptoasn or dbip_asn
	Sources        map[string]fetch.Meta `json:"sources"`
	Warnings       []string              `json:"warnings,omitempty"`
}

// FetchOptions control Fetch.
type FetchOptions struct {
	Config   *config.Config
	CacheDir string
	DataDir  string
	Now      time.Time
	Logf     func(format string, args ...any)
	// Fetcher can be replaced in tests.
	Fetcher *fetch.Fetcher
}

func monthURL(tmpl string, t time.Time) string {
	r := strings.NewReplacer("{YYYY}", t.Format("2006"), "{MM}", t.Format("01"))
	return r.Replace(tmpl)
}

// fetchMonthly tries the current month first and falls back to the previous
// month, because DB-IP publishes the new file during the first day.
func fetchMonthly(ctx context.Context, f *fetch.Fetcher, name, tmpl string, now time.Time) (fetch.Meta, string, error) {
	cur := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, t := range []time.Time{cur, cur.AddDate(0, -1, 0)} {
		_, m, err := f.Fetch(ctx, name, monthURL(tmpl, t))
		if err == nil {
			return m, t.Format("2006-01"), nil
		}
		if !errors.Is(err, fetch.ErrNotFound) {
			return fetch.Meta{}, "", err
		}
	}
	return fetch.Meta{}, "", fmt.Errorf("%s: neither %s nor the previous month is available", name, cur.Format("2006-01"))
}

// Fetch downloads every enabled daily source and writes inputs.json.
func Fetch(ctx context.Context, opt FetchOptions, outPath string) (*InputsFile, error) {
	cfg := opt.Config
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	f := opt.Fetcher
	if f == nil {
		f = fetch.New(opt.CacheDir, cfg.UserAgent())
		f.Logf = opt.Logf
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}
	in := &InputsFile{BuilderVersion: BuilderVersion, Sources: map[string]fetch.Meta{}}
	var errs []error
	// reuse falls back to the cached copy of a source when today's download
	// failed, if that copy is recent enough. It is used for everything except
	// the DB-IP base, so that a flaky or reorganised download page (Azure
	// changes its JSON link every week) does not stop the daily release; the
	// warning makes the run open an issue.
	reuse := func(name string, cause error) bool {
		_, m, ok := f.Cached(name)
		if !ok {
			return false
		}
		t, err := time.Parse(time.RFC3339, m.FetchedAt)
		if err != nil || opt.Now.Sub(t) > MaxStaleAge {
			return false
		}
		in.Sources[name] = m
		in.Warnings = append(in.Warnings, fmt.Sprintf("%s download failed, reused the copy fetched at %s: %v", name, m.FetchedAt, cause))
		opt.Logf("%s: download failed, reusing the copy fetched at %s", name, m.FetchedAt)
		return true
	}
	for _, name := range config.DailySources {
		if !cfg.Enabled(name) {
			continue
		}
		url := cfg.Sources[name]
		switch name {
		case config.SrcDBIPCity:
			m, month, err := fetchMonthly(ctx, f, name, url, opt.Now)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			in.Sources[name], in.DBIPMonth = m, month
		case config.SrcIPtoASN:
			_, m, err := f.Fetch(ctx, name, url)
			if err == nil {
				in.Sources[name], in.ASNSource = m, config.SrcIPtoASN
				continue
			}
			opt.Logf("iptoasn failed, falling back to DB-IP ASN Lite: %v", err)
			fm, _, ferr := fetchMonthly(ctx, f, config.SrcDBIPASN, cfg.Sources[config.SrcDBIPASN], opt.Now)
			if ferr != nil {
				switch {
				case reuse(config.SrcIPtoASN, err):
					in.ASNSource = config.SrcIPtoASN
				case reuse(config.SrcDBIPASN, ferr):
					in.ASNSource = config.SrcDBIPASN
				default:
					errs = append(errs, err, ferr)
				}
				continue
			}
			in.Sources[config.SrcDBIPASN], in.ASNSource = fm, config.SrcDBIPASN
			in.Warnings = append(in.Warnings, fmt.Sprintf("iptoasn download failed, used DB-IP ASN Lite instead: %v", err))
		case config.SrcAzurePage:
			// The JSON file name changes every week; the current link is
			// read from the download page on every run.
			pm, m, err := fetchAzure(ctx, f, url)
			if err != nil {
				if !reuse(config.SrcAzure, err) {
					errs = append(errs, err)
				}
				continue
			}
			in.Sources[name], in.Sources[config.SrcAzure] = pm, m
		default:
			_, m, err := f.Fetch(ctx, name, url)
			if err != nil {
				switch {
				case reuse(name, err):
				case name == config.SrcAPNIC:
					// Only used for the coverage statistics.
					in.Warnings = append(in.Warnings, fmt.Sprintf("%s download failed, coverage statistics are missing from this build: %v", name, err))
				default:
					errs = append(errs, err)
				}
				continue
			}
			in.Sources[name] = m
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("upstream download failed: %w", errors.Join(errs...))
	}
	fp, err := fingerprint(cfg, opt.DataDir, in)
	if err != nil {
		return nil, err
	}
	in.Fingerprint = fp
	if outPath != "" {
		if err := writeJSON(outPath, in); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// MaxStaleAge is how old a cached copy of a source may be when its download
// fails and the cached copy is used instead.
const MaxStaleAge = 14 * 24 * time.Hour

// fetchAzure reads the current ServiceTags_Public JSON link from Microsoft's
// download page and downloads it.
func fetchAzure(ctx context.Context, f *fetch.Fetcher, pageURL string) (page, data fetch.Meta, err error) {
	pagePath, pm, err := f.Fetch(ctx, config.SrcAzurePage, pageURL)
	if err != nil {
		return fetch.Meta{}, fetch.Meta{}, err
	}
	b, err := os.ReadFile(pagePath)
	if err != nil {
		return fetch.Meta{}, fetch.Meta{}, err
	}
	jsonURL, err := sources.FindAzureURL(b)
	if err != nil {
		return fetch.Meta{}, fetch.Meta{}, err
	}
	_, m, err := f.Fetch(ctx, config.SrcAzure, jsonURL)
	if err != nil {
		return fetch.Meta{}, fetch.Meta{}, err
	}
	return pm, m, nil
}

// Sources that do not affect the database content and are therefore left
// out of the fingerprint.
var notFingerprinted = []string{config.SrcAzurePage, config.SrcAPNIC}

// fingerprint hashes everything that determines the database content: the
// builder version, config.json, the curated tables and every upstream file.
func fingerprint(cfg *config.Config, dataDir string, in *InputsFile) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "builder %s\n", BuilderVersion)
	cb, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "config %x\n", sha256.Sum256(cb))
	if dataDir != "" {
		entries, err := os.ReadDir(dataDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".csv") {
				continue
			}
			sum, _, err := fetch.FileSHA256(filepath.Join(dataDir, e.Name()))
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "data %s %s\n", e.Name(), sum)
		}
	}
	names := make([]string, 0, len(in.Sources))
	for n := range in.Sources {
		if !slices.Contains(notFingerprinted, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(h, "source %s %s\n", n, in.Sources[n].SHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ReadInputs reads inputs.json.
func ReadInputs(path string) (*InputsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var in InputsFile
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &in, nil
}

// Changed compares the fingerprint of the current inputs with the one stored
// in the previous release's manifest.json. A missing or unreadable previous
// manifest counts as changed.
func Changed(prevManifest string, in *InputsFile) (bool, string) {
	b, err := os.ReadFile(prevManifest)
	if err != nil {
		return true, "no previous manifest"
	}
	var prev struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(b, &prev); err != nil || prev.Fingerprint == "" {
		return true, "previous manifest has no fingerprint"
	}
	if prev.Fingerprint == in.Fingerprint {
		return false, "inputs unchanged since the previous release"
	}
	return true, "inputs changed"
}
