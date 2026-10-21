package build

import (
	"math"
	"sort"

	"github.com/jinkela1145/open-IPGeo/internal/iprange"
)

// familyAcc accumulates address counts for one address family.
type familyAcc struct {
	is4      bool
	ranges   int
	total    iprange.U128
	bySource map[string]iprange.U128
	anycast  iprange.U128
	cdn      iprange.U128
	cloud    map[string]iprange.U128
	withASN  iprange.U128
	// regionLayer counts what the regional layer did, per evidence, country
	// of the base record and outcome.
	regionLayer map[layerKey]*[outcomes]iprange.U128
	// moves counts the space the regional layer moved, per evidence and
	// region: [0] corrected, [1] filled.
	moves map[moveKey]*[2]iprange.U128

	// China coverage: address space delegated to CN by APNIC.
	delegated   iprange.U128
	delInDB     iprange.U128
	delCountry  map[string]iprange.U128
	delCNLevel  [4]iprange.U128 // by level, only records with country CN
	delCNSource map[string]iprange.U128
	delCNLayer  [outcomes]iprange.U128
}

type layerKey struct {
	via     uint8
	country string
}

type moveKey struct {
	via    uint8
	region int16
}

func newFamilyAcc(is4 bool, delegated []iprange.Range) *familyAcc {
	a := &familyAcc{
		is4:         is4,
		regionLayer: map[layerKey]*[outcomes]iprange.U128{},
		moves:       map[moveKey]*[2]iprange.U128{},
		bySource:    map[string]iprange.U128{},
		cloud:       map[string]iprange.U128{},
		delCountry:  map[string]iprange.U128{},
		delCNSource: map[string]iprange.U128{},
	}
	for _, r := range delegated {
		a.delegated = a.delegated.Add(r.Size())
	}
	return a
}

func (a *familyAcc) add(r *resolver, k key, oc outcome, via uint8, size iprange.U128, inDelegated bool) {
	country, lv, source := r.summary(k)
	a.total = a.total.Add(size)
	a.bySource[source] = a.bySource[source].Add(size)
	if k.flags >= 0 {
		f := r.flagTable[k.flags]
		if f.Anycast {
			a.anycast = a.anycast.Add(size)
		}
		if f.CDN {
			a.cdn = a.cdn.Add(size)
		}
		if f.Cloud != "" {
			a.cloud[f.Cloud] = a.cloud[f.Cloud].Add(size)
		}
	}
	if k.asn >= 0 {
		a.withASN = a.withASN.Add(size)
	}
	if oc != outNone {
		lk := layerKey{via, countryISO(r.base.Records[k.base].Country)}
		l := a.regionLayer[lk]
		if l == nil {
			l = &[outcomes]iprange.U128{}
			a.regionLayer[lk] = l
		}
		l[oc] = l[oc].Add(size)
		if k.prov >= 0 {
			mk := moveKey{via, k.prov}
			m := a.moves[mk]
			if m == nil {
				m = &[2]iprange.U128{}
				a.moves[mk] = m
			}
			i := 0
			if oc == outFilled {
				i = 1
			}
			m[i] = m[i].Add(size)
		}
	}
	if !inDelegated {
		return
	}
	a.delInDB = a.delInDB.Add(size)
	a.delCountry[country] = a.delCountry[country].Add(size)
	if country == "CN" {
		a.delCNLevel[lv] = a.delCNLevel[lv].Add(size)
		a.delCNSource[source] = a.delCNSource[source].Add(size)
		a.delCNLayer[oc] = a.delCNLayer[oc].Add(size)
	}
}

// FamilyReport summarises one family of the full database.
type FamilyReport struct {
	Unit     string             `json:"unit"`
	Ranges   int                `json:"ranges"`
	Total    float64            `json:"total"`
	BySource map[string]float64 `json:"by_source"`
	Anycast  float64            `json:"anycast"`
	CDN      float64            `json:"cdn"`
	Cloud    map[string]float64 `json:"cloud"`
	WithASN  float64            `json:"with_asn"`
	// RegionLayer reports the regional layer decided by a curated ASN, per
	// country: agree / corrected / filled / conflict. GeofeedRegionLayer and
	// WhoisRegionLayer report the pieces decided by an operator's geofeed and
	// by a place name in a RIPE Database object.
	RegionLayer        map[string]map[string]float64 `json:"asn_region_layer"`
	GeofeedRegionLayer map[string]map[string]float64 `json:"geofeed_region_layer,omitempty"`
	WhoisRegionLayer   map[string]map[string]float64 `json:"whois_region_layer,omitempty"`
	// Moves lists the space the regional layer moved, per region and
	// evidence (for the reports, not the manifest).
	Moves []RegionMove `json:"-"`
}

// RegionMove is the space moved into one region on one kind of evidence.
type RegionMove struct {
	Country, Region, Via string
	Corrected, Filled    float64
}

// CoverageReport describes how the address space APNIC delegated to China is
// located in the database. Percentages are of the delegated space.
type CoverageReport struct {
	Unit       string             `json:"unit"`
	Delegated  float64            `json:"delegated"`
	Percent    map[string]float64 `json:"percent"`
	TopCountry map[string]float64 `json:"top_countries_percent"`
}

// Stats is written to manifest.json.
type Stats struct {
	IPv4           FamilyReport   `json:"ipv4"`
	IPv6           FamilyReport   `json:"ipv6"`
	CNCoverageIPv4 CoverageReport `json:"cn_coverage_ipv4"`
	CNCoverageIPv6 CoverageReport `json:"cn_coverage_ipv6"`
	// UnmatchedSubdivisions counts DB-IP subdivision names of countries
	// with a region table that match no row, per country. The names are
	// upstream text for maintainers; they go to a report file in the build
	// artifact, not into the published manifest.
	UnmatchedSubdivisions map[string]map[string]int `json:"-"`
	// LiteAggregation reports the Lite block aggregation per family
	// ("ipv4", "ipv6").
	LiteAggregation map[string]LiteAggStats `json:"lite_aggregation,omitempty"`
}

func unitOf(is4 bool) (string, float64) {
	if is4 {
		return "addresses", 1
	}
	return "/48", math.Ldexp(1, 80)
}

func (a *familyAcc) report(r *resolver) FamilyReport {
	unit, div := unitOf(a.is4)
	conv := func(u iprange.U128) float64 { return round3(u.Float64() / div) }
	fr := FamilyReport{
		Unit: unit, Ranges: a.ranges, Total: conv(a.total),
		BySource: map[string]float64{}, Cloud: map[string]float64{},
		Anycast: conv(a.anycast), CDN: conv(a.cdn), WithASN: conv(a.withASN),
		RegionLayer: map[string]map[string]float64{},
	}
	for lk, l := range a.regionLayer {
		var dst *map[string]map[string]float64
		switch lk.via {
		case viaASN:
			dst = &fr.RegionLayer
		case viaGeofeed:
			dst = &fr.GeofeedRegionLayer
		case viaWhois:
			dst = &fr.WhoisRegionLayer
		default:
			continue
		}
		if *dst == nil {
			*dst = map[string]map[string]float64{}
		}
		(*dst)[lk.country] = map[string]float64{
			"agree":     conv(l[outAgree]),
			"corrected": conv(l[outCorrected]),
			"filled":    conv(l[outFilled]),
			"conflict":  conv(l[outConflict]),
		}
	}
	for mk, m := range a.moves {
		reg := r.regions[mk.region]
		fr.Moves = append(fr.Moves, RegionMove{Country: reg.Country, Region: reg.ISO, Via: viaNames[mk.via],
			Corrected: conv(m[0]), Filled: conv(m[1])})
	}
	sort.Slice(fr.Moves, func(i, j int) bool {
		x, y := fr.Moves[i], fr.Moves[j]
		if x.Country != y.Country {
			return x.Country < y.Country
		}
		if x.Region != y.Region {
			return x.Region < y.Region
		}
		return x.Via < y.Via
	})
	for k, v := range a.bySource {
		fr.BySource[k] = conv(v)
	}
	for k, v := range a.cloud {
		fr.Cloud[k] = conv(v)
	}
	return fr
}

func (a *familyAcc) coverage() CoverageReport {
	unit, div := unitOf(a.is4)
	cr := CoverageReport{Unit: unit, Delegated: round3(a.delegated.Float64() / div), Percent: map[string]float64{}, TopCountry: map[string]float64{}}
	if a.delegated.IsZero() {
		return cr
	}
	d := a.delegated.Float64()
	pct := func(u iprange.U128) float64 { return round3(100 * u.Float64() / d) }
	cr.Percent["in_database"] = pct(a.delInDB)
	cr.Percent["country_cn"] = pct(a.delCountry["CN"])
	cr.Percent["cn_city"] = pct(a.delCNLevel[levelCity])
	cr.Percent["cn_subdivision_only"] = pct(a.delCNLevel[levelSubdivision])
	cr.Percent["cn_country_only"] = pct(a.delCNLevel[levelCountry].Add(a.delCNLevel[levelNone]))
	cr.Percent["cn_source_dbip"] = pct(a.delCNSource[SourceDBIP])
	cr.Percent["cn_source_bgp_asn"] = pct(a.delCNSource[SourceBGPASN])
	cr.Percent["cn_source_override"] = pct(a.delCNSource[SourceOverride])
	cr.Percent["cn_asn_agree"] = pct(a.delCNLayer[outAgree])
	cr.Percent["cn_asn_corrected"] = pct(a.delCNLayer[outCorrected])
	cr.Percent["cn_asn_filled"] = pct(a.delCNLayer[outFilled])
	cr.Percent["cn_asn_conflict"] = pct(a.delCNLayer[outConflict])
	type kv struct {
		k string
		v iprange.U128
	}
	var list []kv
	for k, v := range a.delCountry {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v.Float64() != list[j].v.Float64() {
			return list[i].v.Float64() > list[j].v.Float64()
		}
		return list[i].k < list[j].k
	})
	for i, e := range list {
		if i == 5 {
			break
		}
		name := e.k
		if name == "" {
			name = "(none)"
		}
		cr.TopCountry[name] = pct(e.v)
	}
	return cr
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
