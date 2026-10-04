package pipeline

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jinkela1145/open-IPGeo/internal/build"
	"github.com/jinkela1145/open-IPGeo/internal/config"
	"github.com/jinkela1145/open-IPGeo/internal/iprange"
	"github.com/jinkela1145/open-IPGeo/internal/sources"
)

func pct(m map[string]float64, k string) string {
	v, ok := m[k]
	if !ok {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}

func coverageTable(b *strings.Builder, title string, c build.CoverageReport) {
	fmt.Fprintf(b, "## %s\n\n", title)
	if c.Delegated == 0 {
		b.WriteString("（本次构建没有 APNIC delegated 数据 / no APNIC delegated data in this build）\n\n")
		return
	}
	fmt.Fprintf(b, "分母 / Denominator: APNIC 分配给 CN 的地址空间 / space delegated to CN = %s %s\n\n", human(c.Delegated), c.Unit)
	b.WriteString("| 指标 / Metric | 占比 / Share |\n|---|---|\n")
	rows := []struct{ label, key string }{
		{"数据库里有记录 / has a record", "in_database"},
		{"国家为 CN / country is CN", "country_cn"},
		{"CN：有城市 / with city", "cn_city"},
		{"CN：只有省 / subdivision only", "cn_subdivision_only"},
		{"CN：只有国家 / country only", "cn_country_only"},
		{"CN：位置来自 DB-IP / located by DB-IP", "cn_source_dbip"},
		{"CN：位置来自省公司 ASN 层 / located by provincial ASN layer", "cn_source_bgp_asn"},
		{"CN：位置来自人工修正 / located by overrides", "cn_source_override"},
		{"ASN 层与 DB-IP 省份一致（保留 DB-IP）/ ASN layer agrees with DB-IP", "cn_asn_agree"},
		{"ASN 层纠正了 DB-IP 的省份（DB-IP 给的是北京）/ ASN layer corrected the province (DB-IP said Beijing)", "cn_asn_corrected"},
		{"ASN 层补上了缺失的省份 / ASN layer filled a missing province", "cn_asn_filled"},
		{"DB-IP 给了别的省，保留 DB-IP / DB-IP names another province, kept", "cn_asn_conflict"},
	}
	for _, r := range rows {
		fmt.Fprintf(b, "| %s | %s |\n", r.label, pct(c.Percent, r.key))
	}
	if len(c.TopCountry) > 0 {
		keys := make([]string, 0, len(c.TopCountry))
		for k := range c.TopCountry {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return c.TopCountry[keys[i]] > c.TopCountry[keys[j]] })
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %.2f%%", k, c.TopCountry[k]))
		}
		fmt.Fprintf(b, "\n这部分地址在库里的国家分布 / Countries of this space in the database: %s\n", strings.Join(parts, ", "))
	}
	b.WriteString("\n")
}

func human(v float64) string {
	switch {
	case v >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 2, 64) + "G"
	case v >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 2, 64) + "M"
	case v >= 1e3:
		return strconv.FormatFloat(v/1e3, 'f', 2, 64) + "K"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func familyTable(b *strings.Builder, title string, f build.FamilyReport) {
	fmt.Fprintf(b, "### %s\n\n", title)
	if f.Total == 0 {
		b.WriteString("（空 / empty）\n\n")
		return
	}
	share := func(v float64) string { return fmt.Sprintf("%.2f%%", 100*v/f.Total) }
	fmt.Fprintf(b, "总量 / Total: %s %s, %d ranges\n\n| 指标 / Metric | 占比 / Share |\n|---|---|\n", human(f.Total), f.Unit, f.Ranges)
	srcs := make([]string, 0, len(f.BySource))
	for k := range f.BySource {
		srcs = append(srcs, k)
	}
	slices.Sort(srcs)
	for _, k := range srcs {
		fmt.Fprintf(b, "| source = %s | %s |\n", k, share(f.BySource[k]))
	}
	fmt.Fprintf(b, "| 有 ASN / with ASN | %s |\n", share(f.WithASN))
	fmt.Fprintf(b, "| 任播 / anycast | %s |\n", share(f.Anycast))
	fmt.Fprintf(b, "| CDN | %s |\n", share(f.CDN))
	clouds := make([]string, 0, len(f.Cloud))
	for k := range f.Cloud {
		clouds = append(clouds, k)
	}
	slices.Sort(clouds)
	for _, k := range clouds {
		fmt.Fprintf(b, "| cloud = %s | %s |\n", k, share(f.Cloud[k]))
	}
	b.WriteString("\n")
}

func accuracyMarkdown(m *Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ACCURACY — %s %s\n\n", m.Name, m.Version)
	b.WriteString("> 自动生成 / Generated automatically. 基于 RIPE Atlas 的误差基准测试会在第 3 阶段加入，这里先给覆盖率。" +
		"\n> The RIPE Atlas error benchmark arrives in phase 3; this file currently reports coverage only.\n\n")
	b.WriteString("「有城市」只表示记录里写了城市，不代表一定准确。\n\"With city\" only means the record names a city; it says nothing about correctness.\n\n")
	coverageTable(&b, "中国 IPv6 覆盖率 / China IPv6 coverage", m.Stats.CNCoverageIPv6)
	coverageTable(&b, "中国 IPv4 覆盖率 / China IPv4 coverage", m.Stats.CNCoverageIPv4)
	b.WriteString("## 全库各层占比 / Layer shares of the whole database\n\n")
	familyTable(&b, "IPv4", m.Stats.IPv4)
	familyTable(&b, "IPv6", m.Stats.IPv6)
	regionLayerTable(&b, m.Stats)
	liteTable(&b, m.Stats.LiteAggregation)
	b.WriteString("## 基准测试 / Benchmark\n\n第 3 阶段加入 / Coming in phase 3.\n")
	return b.String()
}

func mb(size int64) string { return fmt.Sprintf("%.1f MB", float64(size)/1e6) }

// liteBlocks describes the Lite aggregation for the release notes.
func liteBlocks(cfg *config.Config) string {
	v4, v6 := cfg.Lite.MinPrefixV4, cfg.Lite.MinPrefixV6
	if v4 <= 0 && v6 <= 0 {
		return ""
	}
	return fmt.Sprintf("; locations aggregated to IPv4 /%d and IPv6 /%d blocks", v4, v6)
}

// liteTable reports what the Lite aggregation changed.
func liteTable(b *strings.Builder, agg map[string]build.LiteAggStats) {
	if len(agg) == 0 {
		return
	}
	b.WriteString("## Lite 版的合并 / Lite aggregation\n\n")
	b.WriteString("Lite 版把被切碎的小块合并成一个位置：取覆盖地址最多的那个，精度半径放大到能盖住这个块三分之二的地址。" +
		"国家或网络类型不同的块、有空洞的块都原样保留。Full 版不做这种合并。\n" +
		"The Lite edition gives each split block the location that covers most of it and widens the accuracy radius until it covers " +
		"two thirds of the block. Blocks whose parts differ in country or network flags, or that have gaps, are left as they are. " +
		"The full edition is not aggregated.\n\n")
	b.WriteString("| 地址族 / Family | 块大小 / Block | 合并的块 / Merged blocks | 原样保留的碎块 / Split blocks kept | 位置变了的地址 / Addresses moved |\n|---|---|---|---|---|\n")
	for _, fam := range []string{"ipv4", "ipv6"} {
		st, ok := agg[fam]
		if !ok {
			continue
		}
		block := "—"
		if st.PrefixLen > 0 {
			block = fmt.Sprintf("/%d", st.PrefixLen)
		}
		name := map[string]string{"ipv4": "IPv4", "ipv6": "IPv6"}[fam]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %.2f%% |\n", name, block, st.MergedBlocks, st.KeptBlocks, st.MovedPercent)
	}
	b.WriteString("\n")
}

func releaseNotes(cfg *config.Config, m *Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", m.Name, m.Version)
	full, lite := m.Outputs["full"], m.Outputs["lite"]
	fmt.Fprintf(&b, "- `%s` (%s): GeoLite2-City compatible structure + ASN + network flags\n", full.File, mb(full.Size))
	fmt.Fprintf(&b, "- `%s` (%s): map edition (country, coordinates rounded to %g°, accuracy radius tiers, network flags%s)\n",
		lite.File, mb(lite.Size), cfg.Lite.CoordStep, liteBlocks(cfg))
	if gz, ok := m.Outputs["lite_gz"]; ok {
		fmt.Fprintf(&b, "- `%s` (%s): the same Lite database, gzip-compressed, also served through jsDelivr\n", gz.File, mb(gz.Size))
	}
	b.WriteString("\n")
	b.WriteString("Upstream versions:\n\n")
	names := make([]string, 0, len(m.Sources))
	for n := range m.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		s := m.Sources[n]
		v := s.Version
		if v == "" {
			v = "fetched " + s.FetchedAt
		}
		fmt.Fprintf(&b, "- %s: %s (sha256 %s…)\n", n, v, s.SHA256[:min(12, len(s.SHA256))])
	}
	cov := m.Stats.CNCoverageIPv6
	if cov.Delegated > 0 {
		fmt.Fprintf(&b, "\nChina IPv6: %s of the APNIC-delegated space located inside CN, %s with city, %s by the provincial ASN layer.\n",
			pct(cov.Percent, "country_cn"), pct(cov.Percent, "cn_city"), pct(cov.Percent, "cn_source_bgp_asn"))
	}
	if len(m.Warnings) > 0 {
		b.WriteString("\nWarnings:\n\n")
		for _, w := range m.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\nIP Geolocation by DB-IP (https://db-ip.com). Place data © GeoNames (https://www.geonames.org), CC BY 4.0. "+
		"ASN data from iptoasn.com (PDDL 1.0). Data license: CC BY 4.0. See https://github.com/%s/blob/main/SOURCES.md\n", cfg.Repo)
	return b.String()
}

// provinceKeywords maps English place names found in AS descriptions to
// province ISO codes. It is only used to suggest candidates for review.
var provinceKeywords = []struct{ word, iso string }{
	{"BEIJING", "BJ"}, {"TIANJIN", "TJ"}, {"HEBEI", "HE"}, {"SHIJIAZHUANG", "HE"}, {"SHANXI", "SX"}, {"TAIYUAN", "SX"},
	{"NEIMENGGU", "NM"}, {"INNER MONGOLIA", "NM"}, {"INNERMONGOLIA", "NM"}, {"NEI MONGOL", "NM"}, {"HOHHOT", "NM"},
	{"LIAONING", "LN"}, {"SHENYANG", "LN"}, {"DALIAN", "LN"}, {"JILIN", "JL"}, {"CHANGCHUN", "JL"},
	{"HEILONGJIANG", "HL"}, {"HARBIN", "HL"}, {"SHANGHAI", "SH"}, {"JIANGSU", "JS"}, {"NANJING", "JS"}, {"WUXI", "JS"},
	{"ZHEJIANG", "ZJ"}, {"HANGZHOU", "ZJ"}, {"NINGBO", "ZJ"}, {"WENZHOU", "ZJ"}, {"ANHUI", "AH"}, {"HEFEI", "AH"},
	{"FUJIAN", "FJ"}, {"XIAMEN", "FJ"}, {"JIANGXI", "JX"}, {"NANCHANG", "JX"}, {"SHANDONG", "SD"}, {"QINGDAO", "SD"},
	{"JINAN", "SD"}, {"HENAN", "HA"}, {"ZHENGZHOU", "HA"}, {"HUBEI", "HB"}, {"WUHAN", "HB"}, {"HUNAN", "HN"},
	{"CHANGSHA", "HN"}, {"GUANGDONG", "GD"}, {"GUANGZHOU", "GD"}, {"SHENZHEN", "GD"}, {"DONGGUAN", "GD"},
	{"FOSHAN", "GD"}, {"GUANGXI", "GX"}, {"NANNING", "GX"}, {"HAINAN", "HI"}, {"HAIKOU", "HI"}, {"CHONGQING", "CQ"},
	{"SICHUAN", "SC"}, {"CHENGDU", "SC"}, {"GUIZHOU", "GZ"}, {"GUIYANG", "GZ"}, {"YUNNAN", "YN"}, {"KUNMING", "YN"},
	{"XIZANG", "XZ"}, {"TIBET", "XZ"}, {"LHASA", "XZ"}, {"SHAANXI", "SN"}, {"XIAN", "SN"}, {"GANSU", "GS"},
	{"LANZHOU", "GS"}, {"QINGHAI", "QH"}, {"XINING", "QH"}, {"NINGXIA", "NX"}, {"YINCHUAN", "NX"},
	{"XINJIANG", "XJ"}, {"URUMQI", "XJ"},
}

func guessProvince(desc string) string {
	u := strings.ToUpper(desc)
	var found []string
	for _, k := range provinceKeywords {
		if strings.Contains(u, k.word) && !slices.Contains(found, k.iso) {
			found = append(found, k.iso)
		}
	}
	return strings.Join(found, ";")
}

// writeReports writes review material for maintainers (not released).
func writeReports(dir string, l *Loaded, res *build.Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if l.In.ASN != nil {
		for _, t := range config.RegionTables {
			name := strings.ToLower(t.Country) + "_asn_candidates.csv"
			if err := writeASNCandidates(filepath.Join(dir, name), l, t.Country); err != nil {
				return err
			}
		}
	}
	for cc, unmatched := range res.Stats.UnmatchedSubdivisions {
		f, err := os.Create(filepath.Join(dir, "unmatched_"+strings.ToLower(cc)+"_subdivisions.csv"))
		if err != nil {
			return err
		}
		w := csv.NewWriter(f)
		_ = w.Write([]string{"dbip_subdivision_name", "records"})
		names := make([]string, 0, len(unmatched))
		for n := range unmatched {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			_ = w.Write([]string{n, strconv.Itoa(unmatched[n])})
		}
		w.Flush()
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// guessRegion suggests the regions whose English name, capital or DB-IP
// spelling appears in an AS description. It only helps review.
func guessRegion(desc string, regions []sources.Region) string {
	u := strings.ToUpper(desc)
	var found []string
	for _, r := range regions {
		for _, w := range append([]string{r.NameEN, r.CapitalEN}, r.DBIPNames...) {
			w = strings.ToUpper(strings.TrimSpace(w))
			if len(w) < 4 {
				continue
			}
			if i := strings.Index(u, w); i >= 0 && !isLetter(u, i-1) && !isLetter(u, i+len(w)) {
				if !slices.Contains(found, r.ISO) {
					found = append(found, r.ISO)
				}
				break
			}
		}
	}
	return strings.Join(found, ";")
}

func isLetter(s string, i int) bool {
	return i >= 0 && i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z')
}

// writeASNCandidates lists the ASNs registered in country with their address
// space and where DB-IP places that space, to help maintain the country's
// ASN table. The dbip_* columns use IPv4 when the AS has IPv4 space DB-IP
// knows about, otherwise IPv6; labels are region codes of the country's
// admin table, the country code for its records without a known region, and
// other country codes elsewhere.
func writeASNCandidates(path string, l *Loaded, country string) error {
	asn := l.In.ASN
	type agg struct {
		v4, v6     float64
		dist       [2]map[string]float64 // [0] IPv4 addresses, [1] IPv6 /48s
		distTotals [2]float64
	}
	sums := map[int32]*agg{}
	for _, is4 := range []bool{true, false} {
		list := asn.V6
		if is4 {
			list = asn.V4
		}
		for _, s := range list {
			if asn.Infos[s.Val].Country != country {
				continue
			}
			a := sums[s.Val]
			if a == nil {
				a = &agg{dist: [2]map[string]float64{{}, {}}}
				sums[s.Val] = a
			}
			size := s.Range().Size().Float64()
			if is4 {
				a.v4 += size
			} else {
				a.v6 += size / math.Ldexp(1, 80)
			}
		}
	}

	var regions []sources.Region
	provByName := map[string]string{}
	for _, p := range l.In.Regions {
		if p.Country != country {
			continue
		}
		regions = append(regions, p)
		provByName[strings.ToLower(p.NameEN)] = p.ISO
		provByName[strings.ToLower(p.ISO)] = p.ISO
		for _, n := range p.DBIPNames {
			provByName[strings.ToLower(n)] = p.ISO
		}
	}
	labelOf := func(rec *sources.BaseRecord) string {
		if rec.Country == nil {
			return "?"
		}
		if rec.Country.ISOCode != country {
			return rec.Country.ISOCode
		}
		if len(rec.Subdivs) > 0 {
			sd := rec.Subdivs[0]
			for _, n := range []string{sd.ISOCode, sd.Names["en"]} {
				if iso, ok := provByName[strings.ToLower(n)]; ok && n != "" {
					return iso
				}
			}
		}
		return country
	}
	guess := func(desc string) string {
		if country == "CN" {
			return guessProvince(desc)
		}
		return guessRegion(desc, regions)
	}
	labels := make([]string, len(l.In.Base.Records))
	for i := range l.In.Base.Records {
		labels[i] = labelOf(&l.In.Base.Records[i])
	}
	for fi, fam := range []struct {
		base iprange.SegSpans[int32]
		asn  iprange.SegSpans[int32]
		unit float64
	}{{l.In.Base.V4, asn.V4, 1}, {l.In.Base.V6, asn.V6, math.Ldexp(1, 80)}} {
		iprange.Refine(fam.base, []iprange.Spans{fam.asn}, func(s, e netip.Addr, bi int, li []int) {
			if li[0] < 0 {
				return
			}
			a := sums[fam.asn[li[0]].Val]
			if a == nil {
				return
			}
			w := iprange.Range{Start: s, End: e}.Size().Float64() / fam.unit
			a.dist[fi][labels[fam.base[bi].Val]] += w
			a.distTotals[fi] += w
		})
	}

	mapped := map[uint32]string{}
	for _, row := range l.In.RegionASNs {
		if row.Country == country {
			mapped[row.ASN] = row.RegionISO
		}
	}
	ids := make([]int32, 0, len(sums))
	for id := range sums {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := sums[ids[i]], sums[ids[j]]
		if a.v6 != b.v6 {
			return a.v6 > b.v6
		}
		if a.v4 != b.v4 {
			return a.v4 > b.v4
		}
		return asn.Infos[ids[i]].ASN < asn.Infos[ids[j]].ASN
	})
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"asn", "as_description", "ipv4_addresses", "ipv6_48s", "keyword_region", "mapped_region",
		"dbip_basis", "dbip_top", "dbip_top_share", "dbip_second", "dbip_second_share", "dbip_agrees_share"})
	share := func(v, total float64) string { return strconv.FormatFloat(v/total, 'f', 3, 64) }
	for _, id := range ids {
		info := asn.Infos[id]
		a := sums[id]
		row := []string{strconv.FormatUint(uint64(info.ASN), 10), info.Org,
			strconv.FormatFloat(a.v4, 'f', 0, 64), strconv.FormatFloat(a.v6, 'f', 2, 64),
			guess(info.Org), mapped[info.ASN]}
		fi := 0
		if a.distTotals[0] == 0 {
			fi = 1
		}
		total := a.distTotals[fi]
		if total == 0 {
			row = append(row, "", "", "", "", "", "")
		} else {
			type kv struct {
				k string
				v float64
			}
			var top []kv
			for k, v := range a.dist[fi] {
				top = append(top, kv{k, v})
			}
			sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v || top[i].v == top[j].v && top[i].k < top[j].k })
			basis := map[int]string{0: "ipv4", 1: "ipv6"}[fi]
			row = append(row, basis, top[0].k, share(top[0].v, total))
			if len(top) > 1 {
				row = append(row, top[1].k, share(top[1].v, total))
			} else {
				row = append(row, "", "")
			}
			if p := mapped[info.ASN]; p != "" {
				row = append(row, share(a.dist[fi][p], total))
			} else {
				row = append(row, "")
			}
		}
		_ = w.Write(row)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// regionLayerTable reports what the regional ASN layer did per country.
func regionLayerTable(b *strings.Builder, st build.Stats) {
	if len(st.IPv4.RegionLayer) == 0 && len(st.IPv6.RegionLayer) == 0 {
		return
	}
	b.WriteString("## 地区 ASN 层 / Regional ASN layer\n\n")
	b.WriteString("网段的起源 AS 属于只服务一个地区的网络（中国的省公司、俄罗斯的地区运营商）时：DB-IP 给出运营商总部所在地" +
		"（北京 / 莫斯科）或没有给地区，就改成这个地区；DB-IP 给了别的地区就保留 DB-IP。\n" +
		"When the origin AS of a prefix serves a single region and DB-IP puts the prefix at the carriers' headquarters " +
		"(Beijing / Moscow) or in no region, the region is replaced; when DB-IP names another region, DB-IP is kept.\n\n")
	b.WriteString("| 地址族 / Family | 国家 / Country | 一致 / agree | 纠正 / corrected | 补上 / filled | 保留 DB-IP / kept |\n|---|---|---|---|---|---|\n")
	for _, fam := range []struct {
		name string
		fr   build.FamilyReport
	}{{"IPv4", st.IPv4}, {"IPv6", st.IPv6}} {
		ccs := make([]string, 0, len(fam.fr.RegionLayer))
		for cc := range fam.fr.RegionLayer {
			ccs = append(ccs, cc)
		}
		slices.Sort(ccs)
		for _, cc := range ccs {
			l := fam.fr.RegionLayer[cc]
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n", fam.name, cc,
				human(l["agree"]), human(l["corrected"]), human(l["filled"]), human(l["conflict"]))
		}
	}
	b.WriteString("\n单位：IPv4 为地址数，IPv6 为 /48 个数。/ Units: addresses for IPv4, /48s for IPv6.\n\n")
}
