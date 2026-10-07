package ripe

import (
	"fmt"
	"math/bits"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/jinkela1145/open-IPGeo/internal/iprange"
)

// Evidence kinds, also written to the "source" field of the records they
// locate.
const (
	ViaGeofeed = "geofeed"
	ViaWhois   = "whois"
)

// Country is a country covered by the layer.
type Country struct {
	Code    string          // ISO 3166-1 alpha-2
	Vocab   *Vocab          // place names of the country
	Regions map[string]bool // region codes of the region table (without "RU-")
}

// Hint is region evidence for a range.
type Hint struct {
	R       iprange.Range
	Country string
	Region  string // region code of the region table; empty when Block is set
	Via     string // ViaGeofeed or ViaWhois
	// Block is set for geofeed entries that place the range in another
	// country: the regional layer leaves such ranges alone.
	Block bool
	// Rank orders overlapping hints: geofeeds above whois, then the more
	// specific range.
	Rank int
	// Word is the place name a whois hint was found by (for review).
	Word string
}

// Stats describes what Collect found. It is written to manifest.json, so it
// holds counts only.
type Stats struct {
	Objects        int            `json:"objects"`
	CountryObjects map[string]int `json:"country_objects"`
	WhoisHints     map[string]int `json:"whois_hints"`
	GeofeedURLs    int            `json:"geofeed_urls"`
	GeofeedsRead   int            `json:"geofeeds_read"`
	GeofeedErrors  int            `json:"geofeed_unreadable"`
	Entries        int            `json:"geofeed_entries"`
	Accepted       map[string]int `json:"geofeed_accepted"`
	CountryOnly    int            `json:"geofeed_country_only"`
	OtherCountry   int            `json:"geofeed_other_country"`
	NoCountry      int            `json:"geofeed_no_country"`
	Outside        int            `json:"geofeed_outside_object"`
	BadLines       int            `json:"geofeed_bad_lines"`
	// UnmatchedRegions counts region (or city) values of geofeed entries in
	// a covered country that match no region, per country.
	UnmatchedRegions map[string]map[string]int `json:"-"`
}

// MaxGeofeedEntries bounds the entries read from one geofeed.
const MaxGeofeedEntries = 2_000_000

func coveredCountry(o *Object, byCode map[string]int) int {
	for _, c := range o.Countries {
		if i, ok := byCode[c]; ok {
			return i
		}
	}
	return -1
}

// GeofeedURLs returns the distinct geofeeds referenced by objects of the
// given countries, sorted.
func GeofeedURLs(splitFiles []string, countries []string) ([]string, error) {
	byCode := map[string]int{}
	for i, c := range countries {
		byCode[c] = i
	}
	set := map[string]bool{}
	for _, p := range splitFiles {
		err := Scan(p, func(o *Object) error {
			if o.Geofeed != "" && coveredCountry(o, byCode) >= 0 {
				set[o.Geofeed] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	slices.Sort(out)
	return out, nil
}

type ref struct {
	r       iprange.Range
	country int
}

// refIndex answers whether a range lies inside one of a set of ranges.
type refIndex struct {
	refs   []ref
	maxEnd []int // index into refs of the largest end among refs[:i+1]
}

func newRefIndex(refs []ref) *refIndex {
	slices.SortFunc(refs, func(a, b ref) int { return a.r.Start.Compare(b.r.Start) })
	ix := &refIndex{refs: refs, maxEnd: make([]int, len(refs))}
	for i := range refs {
		ix.maxEnd[i] = i
		if i > 0 && refs[ix.maxEnd[i-1]].r.End.Compare(refs[i].r.End) > 0 {
			ix.maxEnd[i] = ix.maxEnd[i-1]
		}
	}
	return ix
}

// containing returns the referencing object that contains r, or nil.
func (ix *refIndex) containing(r iprange.Range) *ref {
	i := sort.Search(len(ix.refs), func(i int) bool { return ix.refs[i].r.Start.Compare(r.Start) > 0 }) - 1
	if i < 0 {
		return nil
	}
	best := &ix.refs[ix.maxEnd[i]]
	if best.r.End.Compare(r.End) < 0 {
		return nil
	}
	return best
}

// Collect reads the split files and the geofeeds available through
// geofeedPath (the cached copy of a URL, if any) and returns the hints of the
// covered countries in a deterministic order.
func Collect(splitFiles []string, countries []Country, geofeedPath func(url string) (string, bool)) ([]Hint, *Stats, error) {
	st := &Stats{
		CountryObjects: map[string]int{}, WhoisHints: map[string]int{}, Accepted: map[string]int{},
		UnmatchedRegions: map[string]map[string]int{},
	}
	byCode := map[string]int{}
	for i, c := range countries {
		byCode[c.Code] = i
	}
	var hints []Hint
	feeds := map[string][]ref{} // geofeed URL -> referencing objects
	for _, p := range splitFiles {
		err := Scan(p, func(o *Object) error {
			st.Objects++
			ci := coveredCountry(o, byCode)
			if ci < 0 {
				return nil
			}
			c := &countries[ci]
			st.CountryObjects[c.Code]++
			if o.Geofeed != "" {
				feeds[o.Geofeed] = append(feeds[o.Geofeed], ref{o.R, ci})
			}
			texts := make([]string, 0, 1+len(o.Descr))
			texts = append(append(texts, o.Netname), o.Descr...)
			if region, word := c.Vocab.Match(false, texts...); region != "" && c.Regions[region] {
				st.WhoisHints[c.Code]++
				hints = append(hints, Hint{R: o.R, Country: c.Code, Region: region, Via: ViaWhois, Rank: Rank(ViaWhois, o.R), Word: word})
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	urls := make([]string, 0, len(feeds))
	for u := range feeds {
		urls = append(urls, u)
	}
	slices.Sort(urls)
	st.GeofeedURLs = len(urls)
	for _, u := range urls {
		path, ok := geofeedPath(u)
		if !ok {
			continue
		}
		entries, bad, err := readGeofeed(path)
		if err != nil {
			st.GeofeedErrors++
			continue
		}
		st.GeofeedsRead++
		st.BadLines += bad
		var v4, v6 []ref
		for _, r := range feeds[u] {
			if r.r.Is4() {
				v4 = append(v4, r)
			} else {
				v6 = append(v6, r)
			}
		}
		ix4, ix6 := newRefIndex(v4), newRefIndex(v6)
		for _, e := range entries {
			st.Entries++
			r := iprange.FromPrefix(e.P)
			ix := ix6
			if r.Is4() {
				ix = ix4
			}
			owner := ix.containing(r)
			if owner == nil {
				st.Outside++
				continue
			}
			c := &countries[owner.country]
			switch {
			case e.Country == "":
				st.NoCountry++
			case e.Country != c.Code:
				// The operator places this range in another country.
				st.OtherCountry++
				hints = append(hints, Hint{R: r, Via: ViaGeofeed, Block: true, Rank: Rank(ViaGeofeed, r)})
			default:
				region := c.regionOf(e)
				if region == "" {
					st.CountryOnly++
					if key := firstNonEmpty(e.Region, e.City); key != "" {
						m := st.UnmatchedRegions[c.Code]
						if m == nil {
							m = map[string]int{}
							st.UnmatchedRegions[c.Code] = m
						}
						m[key]++
					}
					continue
				}
				st.Accepted[c.Code]++
				hints = append(hints, Hint{R: r, Country: c.Code, Region: region, Via: ViaGeofeed, Rank: Rank(ViaGeofeed, r)})
			}
		}
	}
	return hints, st, nil
}

func readGeofeed(path string) ([]Entry, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	return ParseGeofeed(f, MaxGeofeedEntries)
}

// regionOf maps the region (ISO 3166-2 code or name) or, failing that, the
// city of a geofeed entry to a region of the table.
func (c *Country) regionOf(e Entry) string {
	if code := strings.TrimPrefix(e.Region, c.Code+"-"); code != "" && c.Regions[code] {
		return code
	}
	for _, t := range []string{e.Region, e.City} {
		if t == "" {
			continue
		}
		if region, _ := c.Vocab.Match(true, t); region != "" && c.Regions[region] {
			return region
		}
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// Rank orders hints: every geofeed hint outranks every whois hint; within a
// kind the smaller range wins.
func Rank(via string, r iprange.Range) int {
	pri := 1
	if via == ViaGeofeed {
		pri = 2
	}
	maxBits := 128
	if r.Is4() {
		maxBits = 32
	}
	return pri*1000 + maxBits - log2(r.Size())
}

func log2(u iprange.U128) int {
	if u.Hi != 0 {
		return 64 + bits.Len64(u.Hi) - 1
	}
	if u.Lo == 0 {
		return 128 // the whole IPv6 space overflows to zero
	}
	return bits.Len64(u.Lo) - 1
}

// String summarises the statistics for the build log.
func (s *Stats) String() string {
	return fmt.Sprintf("%d objects (%v covered), whois hints %v, geofeeds %d referenced / %d read, entries %d: accepted %v, "+
		"country only %d, other country %d, outside the object %d, bad lines %d",
		s.Objects, s.CountryObjects, s.WhoisHints, s.GeofeedURLs, s.GeofeedsRead, s.Entries, s.Accepted,
		s.CountryOnly, s.OtherCountry, s.Outside, s.BadLines)
}
