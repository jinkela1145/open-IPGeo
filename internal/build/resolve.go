package build

import (
	"fmt"
	"math"
	"strings"

	"github.com/jinkela1145/open-IPGeo/internal/config"
	"github.com/jinkela1145/open-IPGeo/internal/sources"
)

// Location sources written to the "source" field.
const (
	SourceDBIP     = "dbip"
	SourceBGPASN   = "bgp-asn"
	SourceOverride = "override"
)

// outcome describes what the regional ASN layer did to a piece.
//
// The layer only acts where DB-IP names the same country as the region
// table. It overrides DB-IP when DB-IP gives no subdivision, or names a
// subdivision marked CarrierHQ: DB-IP places much of the national carriers'
// space where their headquarters are (Beijing holds over a quarter of DB-IP's
// Chinese IPv4 space in 2026). When DB-IP names any other subdivision it
// probably knows something specific, and is kept; so is a subdivision name
// the region table does not know (a variant spelling, or a territory the
// table deliberately leaves out).
type outcome uint8

const (
	outNone      outcome = iota // layer not applicable
	outAgree                    // DB-IP already in the AS's region: kept DB-IP
	outCorrected                // DB-IP at the carriers' headquarters: replaced
	outFilled                   // DB-IP had no subdivision: filled
	outConflict                 // DB-IP names another or an unknown subdivision: kept DB-IP
	outcomes
)

// Values of resolver.baseRegion besides region indexes.
const (
	noSubdivision      = -1
	unknownSubdivision = -2
)

// key identifies the final record of a piece of address space.
type key struct {
	base  int32
	asn   int32 // index into ASNData.Infos, -1 if none
	flags int32 // index into resolver.flagTable, -1 if none
	prov  int16 // region applied by the regional ASN layer, -1 if none
	over  int32 // index into resolver.overrides, -1 if none
}

type override struct {
	src       sources.Override
	country   *sources.Country
	continent *sources.Continent
	subdivs   []*sources.Place
	city      *sources.Place
	lat, lon  float64
	hasLoc    bool
	radius    uint16
	level     level
}

type level uint8

const (
	levelNone level = iota
	levelCountry
	levelSubdivision
	levelCity
)

func (l level) String() string {
	return [...]string{"none", "country", "subdivision", "city"}[l]
}

type resolver struct {
	cfg         *config.Config
	base        *sources.BaseData
	asn         *sources.ASNData
	regions     []sources.Region
	regionPlace []*sources.Place
	regionSubs  [][]*sources.Place
	byCountry   map[string][]int16 // region indexes per country
	asnRegion   map[uint32]int16
	asnFlags    map[uint32]sources.NetFlags
	flagTable   []sources.NetFlags
	flagIndex   map[sources.NetFlags]int32
	baseRegion  []int16 // per base record: region index, noSubdivision or unknownSubdivision
	overrides   []override

	// unmatched counts DB-IP subdivision names of covered countries that no
	// region table row matches, per country.
	unmatched map[string]map[string]int
}

func newResolver(in *Inputs, cfg *config.Config) (*resolver, error) {
	r := &resolver{
		cfg:        cfg,
		base:       in.Base,
		asn:        in.ASN,
		regions:    in.Regions,
		byCountry:  map[string][]int16{},
		asnRegion:  map[uint32]int16{},
		asnFlags:   in.ASNFlags,
		flagIndex:  map[sources.NetFlags]int32{},
		baseRegion: make([]int16, len(in.Base.Records)),
		unmatched:  map[string]map[string]int{},
	}
	byISO := map[string]int16{}
	for i, p := range in.Regions {
		key := p.Country + "-" + p.ISO
		if _, dup := byISO[key]; dup {
			return nil, fmt.Errorf("region %s is listed twice", key)
		}
		byISO[key] = int16(i)
		r.byCountry[p.Country] = append(r.byCountry[p.Country], int16(i))
		names := sources.Names{"en": p.NameEN}
		if p.Lang != "" && p.NameLocal != "" {
			names[p.Lang] = p.NameLocal
		}
		place := &sources.Place{GeonameID: p.GeonameID, ISOCode: p.ISO, Names: names}
		r.regionPlace = append(r.regionPlace, place)
		r.regionSubs = append(r.regionSubs, []*sources.Place{place})
	}
	r.matchBaseRegions()
	for _, row := range in.RegionASNs {
		idx, ok := byISO[row.Country+"-"+row.RegionISO]
		if !ok {
			return nil, fmt.Errorf("%s ASN table: AS%d refers to unknown region %q (add it to the %s admin table)",
				row.Country, row.ASN, row.RegionISO, row.Country)
		}
		if prev, dup := r.asnRegion[row.ASN]; dup && prev != idx {
			return nil, fmt.Errorf("AS%d is listed for both %s-%s and %s-%s", row.ASN,
				r.regions[prev].Country, r.regions[prev].ISO, row.Country, row.RegionISO)
		}
		r.asnRegion[row.ASN] = idx
	}
	for _, o := range in.Overrides {
		ro, err := r.prepareOverride(o, in.Cities)
		if err != nil {
			return nil, err
		}
		r.overrides = append(r.overrides, ro)
	}
	return r, nil
}

func (r *resolver) internFlags(f sources.NetFlags) int32 {
	if f.IsZero() {
		return -1
	}
	if id, ok := r.flagIndex[f]; ok {
		return id
	}
	id := int32(len(r.flagTable))
	r.flagTable = append(r.flagTable, f)
	r.flagIndex[f] = id
	return id
}

// normalizeCN strips common administrative suffixes from Chinese names.
func normalizeCN(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range []string{"维吾尔自治区", "壮族自治区", "回族自治区", "特别行政区", "自治区", "省", "市"} {
		if strings.HasSuffix(s, suf) && len([]rune(s)) > len([]rune(suf)) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// findRegion returns the index of the region of country called name (its
// ISO code, English or local name, or a DB-IP spelling), or -1.
func (r *resolver) findRegion(country, name string) int16 {
	if name == "" {
		return -1
	}
	n := strings.TrimSpace(name)
	cn := normalizeCN(n)
	for _, i := range r.byCountry[country] {
		p := &r.regions[i]
		if strings.EqualFold(p.ISO, n) || strings.EqualFold(p.NameEN, n) || strings.EqualFold(p.NameLocal, n) ||
			country == "CN" && normalizeCN(p.NameLocal) == cn {
			return i
		}
		for _, alias := range p.DBIPNames {
			if strings.EqualFold(alias, n) {
				return i
			}
		}
	}
	return -1
}

// matchBaseRegions maps the first subdivision of every base record of a
// covered country to a region and counts the DB-IP names that match none.
func (r *resolver) matchBaseRegions() {
	type memo struct {
		country string
		place   *sources.Place
	}
	seen := map[memo]int16{}
	for i := range r.base.Records {
		rec := &r.base.Records[i]
		r.baseRegion[i] = noSubdivision
		if rec.Country == nil || len(r.byCountry[rec.Country.ISOCode]) == 0 || len(rec.Subdivs) == 0 {
			continue
		}
		cc, s := rec.Country.ISOCode, rec.Subdivs[0]
		p, ok := seen[memo{cc, s}]
		if !ok {
			p = unknownSubdivision
			for _, cand := range []string{s.ISOCode, s.Names["en"], s.Names["zh-CN"], s.Names["ru"]} {
				if idx := r.findRegion(cc, cand); idx >= 0 {
					p = idx
					break
				}
			}
			seen[memo{cc, s}] = p
		}
		if p == unknownSubdivision {
			if r.unmatched[cc] == nil {
				r.unmatched[cc] = map[string]int{}
			}
			r.unmatched[cc][s.Names["en"]]++
		}
		r.baseRegion[i] = p
	}
}

func (r *resolver) prepareOverride(o sources.Override, cities []sources.CNCity) (override, error) {
	fail := func(format string, args ...any) (override, error) {
		return override{}, fmt.Errorf("overrides.csv line %d: %s", o.Line, fmt.Sprintf(format, args...))
	}
	ro := override{src: o, country: r.base.Countries[o.Country], continent: r.base.ContinentOf[o.Country]}
	if ro.country == nil {
		return fail("country %s does not exist in the base database", o.Country)
	}
	ro.level = levelCountry
	var provCoord, cityCoord [2]float64
	var haveProvCoord, haveCityCoord bool
	if o.Province != "" {
		if o.Country == "CN" {
			pi := r.findRegion("CN", o.Province)
			if pi < 0 {
				return fail("province %q is not in cn_admin.csv", o.Province)
			}
			p := r.regions[pi]
			ro.subdivs = []*sources.Place{r.regionPlace[pi]}
			provCoord, haveProvCoord = [2]float64{p.Lat, p.Lon}, true
			if o.City != "" {
				var found *sources.CNCity
				for i := range cities {
					c := &cities[i]
					if c.ProvinceISO == p.ISO && (normalizeCN(c.NameZH) == normalizeCN(o.City) || strings.EqualFold(c.NameEN, o.City)) {
						found = c
						break
					}
				}
				if found == nil {
					return fail("city %q of %s is not in cn_cities.csv", o.City, p.NameLocal)
				}
				ro.city = &sources.Place{GeonameID: found.GeonameID, Names: sources.Names{"en": found.NameEN, "zh-CN": found.NameZH}}
				cityCoord, haveCityCoord = [2]float64{found.Lat, found.Lon}, true
			}
		} else {
			ro.subdivs = []*sources.Place{{Names: sources.Names{"en": o.Province}}}
			if o.City != "" {
				ro.city = &sources.Place{Names: sources.Names{"en": o.City}}
			}
		}
		ro.level = levelSubdivision
		if ro.city != nil {
			ro.level = levelCity
		}
	}
	switch {
	case o.HasLoc:
		ro.lat, ro.lon, ro.hasLoc = o.Lat, o.Lon, true
	case haveCityCoord:
		ro.lat, ro.lon, ro.hasLoc = cityCoord[0], cityCoord[1], true
	case haveProvCoord:
		ro.lat, ro.lon, ro.hasLoc = provCoord[0], provCoord[1], true
	default:
		return fail("latitude/longitude are required unless the place is in cn_admin.csv / cn_cities.csv")
	}
	switch {
	case o.RadiusKM > 0:
		ro.radius = o.RadiusKM
	case ro.level == levelCity:
		ro.radius = r.cfg.RadiusKM.City
	case ro.level == levelSubdivision:
		ro.radius = r.cfg.RadiusKM.Subdivision
	default:
		ro.radius = r.cfg.RadiusKM.Country
	}
	return ro, nil
}

// keyFor computes the record key of a piece.
func (r *resolver) keyFor(baseIdx, asnIdx, listFlags, overIdx int32) (key, outcome) {
	k := key{base: baseIdx, asn: asnIdx, flags: -1, prov: -1, over: overIdx}
	var f sources.NetFlags
	if listFlags >= 0 {
		f = r.flagTable[listFlags]
	}
	oc := outNone
	if asnIdx >= 0 {
		info := &r.asn.Infos[asnIdx]
		if af, ok := r.asnFlags[info.ASN]; ok {
			f = f.Merge(af)
		}
		if p, ok := r.asnRegion[info.ASN]; ok {
			rec := &r.base.Records[baseIdx]
			if rec.Country != nil && rec.Country.ISOCode == r.regions[p].Country {
				switch bp := r.baseRegion[baseIdx]; {
				case bp == p:
					oc = outAgree
				case bp == noSubdivision:
					k.prov, oc = p, outFilled
				case bp >= 0 && r.regions[bp].CarrierHQ:
					k.prov, oc = p, outCorrected
				default:
					oc = outConflict
				}
			}
		}
	}
	k.flags = r.internFlags(f)
	return k, oc
}

// resolved is the final content of a record.
type resolved struct {
	continent *sources.Continent
	country   *sources.Country
	subdivs   []*sources.Place
	city      *sources.Place
	lat, lon  float64
	hasLoc    bool
	radius    uint16
	timeZone  string
	source    string
	level     level
	flags     sources.NetFlags
	asn       *sources.ASNInfo
}

func (r *resolver) resolve(k key) resolved {
	rec := &r.base.Records[k.base]
	out := resolved{
		continent: rec.Continent, country: rec.Country, subdivs: rec.Subdivs, city: rec.City,
		lat: rec.Lat, lon: rec.Lon, hasLoc: rec.HasLoc, timeZone: rec.TimeZone, source: SourceDBIP,
	}
	switch {
	case rec.City != nil:
		out.level, out.radius = levelCity, r.cfg.RadiusKM.City
	case len(rec.Subdivs) > 0:
		out.level, out.radius = levelSubdivision, r.cfg.RadiusKM.Subdivision
	case rec.Country != nil:
		out.level, out.radius = levelCountry, r.cfg.RadiusKM.Country
	}
	if rec.Radius > 0 {
		out.radius = rec.Radius
	}
	if k.prov >= 0 {
		p := r.regions[k.prov]
		out.subdivs = r.regionSubs[k.prov]
		out.city = nil
		out.lat, out.lon, out.hasLoc = p.Lat, p.Lon, true
		out.radius = p.RadiusKM
		out.timeZone = ""
		out.level = levelSubdivision
		out.source = SourceBGPASN
	}
	if k.flags >= 0 {
		out.flags = r.flagTable[k.flags]
		if out.flags.Anycast && out.radius < r.cfg.RadiusKM.Anycast {
			out.radius = r.cfg.RadiusKM.Anycast
		}
	}
	if k.over >= 0 {
		o := &r.overrides[k.over]
		if o.country.ISOCode != countryISO(out.country) {
			out.timeZone = ""
		}
		out.country, out.continent = o.country, o.continent
		out.subdivs, out.city = o.subdivs, o.city
		out.lat, out.lon, out.hasLoc = o.lat, o.lon, o.hasLoc
		out.radius, out.level, out.source = o.radius, o.level, SourceOverride
	}
	if k.asn >= 0 {
		out.asn = &r.asn.Infos[k.asn]
	}
	return out
}

// summary is a cheap view of a key used for statistics.
func (r *resolver) summary(k key) (country string, lv level, source string) {
	if k.over >= 0 {
		o := &r.overrides[k.over]
		return o.country.ISOCode, o.level, SourceOverride
	}
	rec := &r.base.Records[k.base]
	country = countryISO(rec.Country)
	if k.prov >= 0 {
		return country, levelSubdivision, SourceBGPASN
	}
	switch {
	case rec.City != nil:
		lv = levelCity
	case len(rec.Subdivs) > 0:
		lv = levelSubdivision
	case rec.Country != nil:
		lv = levelCountry
	}
	return country, lv, SourceDBIP
}

func countryISO(c *sources.Country) string {
	if c == nil {
		return ""
	}
	return c.ISOCode
}

// roundTo rounds v to the nearest multiple of step and returns the multiple
// count.
func roundTo(v, step float64) int32 {
	return int32(math.Round(v / step))
}

func radiusTier(r uint16, tiers []uint16) uint16 {
	for _, t := range tiers {
		if r <= t {
			return t
		}
	}
	return tiers[len(tiers)-1]
}
