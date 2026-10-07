package ripe

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jinkela1145/open-IPGeo/internal/iprange"
)

const splitSample = "%  RIPE split file\r\n" +
	"\r\n" +
	"inetnum:        5.0.0.0 - 5.0.255.255\r\n" +
	"netname:        BIG-NET\r\n" +
	"descr:          First line,\r\n" +
	"+               continued\r\n" +
	"descr:          Second\r\n" +
	"\t        tab continued\r\n" +
	"country:        RU # comment\r\n" +
	"country:        de\r\n" +
	"geofeed:        https://example.net/feed.csv\r\n" +
	"remarks:        Geofeed https://example.net/feed.csv\r\n" +
	"source:         RIPE\r\n" +
	"\r\n" +
	"route:          5.0.0.0/16\r\n" +
	"descr:          not an inetnum\r\n" +
	"origin:         AS1\r\n" +
	"\r\n" +
	"inetnum:        5.1.0.0-5.1.0.255\r\n" +
	"netname:        NO-SPACES\r\n" +
	"country:        RU\r\n" +
	"remarks:        GEOFEED https://example.org/a.csv\r\n" +
	"\r\n" +
	"inetnum:        5.2.0.0 - 5.2.0.255\r\n" +
	"geofeed:        https://example.org/a.csv\r\n" +
	"geofeed:        https://example.org/b.csv\r\n" +
	"\r\n" +
	"inetnum:        5.3.0.0 - 5.3.0.255\r\n" +
	"geofeed:        http://example.org/plain.csv\r\n" +
	"\r\n" +
	"inetnum:        not a range\r\n" +
	"netname:        BROKEN\r\n" +
	"\r\n" +
	"inetnum:        2001:db8::/32\r\n" +
	"netname:        WRONG-FAMILY\r\n" +
	"\r\n" +
	"inet6num:       2a00:1::/32\r\n" +
	"netname:        V6-NET\r\n" +
	"descr:          Caf\xe9\r\n" +
	"country:        RU"

func scanAll(t *testing.T, data string) []Object {
	t.Helper()
	var out []Object
	err := ScanReader(strings.NewReader(data), func(o *Object) error {
		c := *o
		c.Countries = slices.Clone(o.Countries)
		c.Descr = slices.Clone(o.Descr)
		out = append(out, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScanReader(t *testing.T) {
	objs := scanAll(t, splitSample)
	if len(objs) != 5 {
		t.Fatalf("got %d objects: %+v", len(objs), objs)
	}
	big := objs[0]
	if big.R.String() != "5.0.0.0-5.0.255.255" || big.Netname != "BIG-NET" ||
		!slices.Equal(big.Descr, []string{"First line, continued", "Second tab continued"}) ||
		!slices.Equal(big.Countries, []string{"RU", "DE"}) || big.Geofeed != "https://example.net/feed.csv" || !big.HasCountry("DE") {
		t.Errorf("first object: %+v", big)
	}
	if objs[1].R.String() != "5.1.0.0-5.1.0.255" || objs[1].Geofeed != "https://example.org/a.csv" {
		t.Errorf("remarks geofeed: %+v", objs[1])
	}
	if objs[2].Geofeed != "" {
		t.Errorf("two different geofeeds must be ignored: %+v", objs[2])
	}
	if objs[3].Geofeed != "" {
		t.Errorf("plain HTTP geofeed must be ignored: %+v", objs[3])
	}
	v6 := objs[4]
	if v6.R.String() != "2a00:1::-2a00:1:ffff:ffff:ffff:ffff:ffff:ffff" || v6.Descr[0] != "Café" || !v6.HasCountry("RU") {
		t.Errorf("inet6num: %+v", v6)
	}
}

func TestScanGzip(t *testing.T) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(splitSample))
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "split.gz")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := Scan(p, func(*Object) error { n++; return nil }); err != nil || n != 5 {
		t.Fatalf("Scan: %d objects, %v", n, err)
	}
}

func TestValidGeofeedURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.net/geofeed.csv": true,
		"https://example.net:8443/g":      true,
		"http://example.net/geofeed.csv":  false,
		"https://user@example.net/g":      false,
		"https:///g":                      false,
		"ftp://example.net/g":             false,
		"example.net/g":                   false,
	} {
		if got := ValidGeofeedURL(u); got != want {
			t.Errorf("ValidGeofeedURL(%q) = %v", u, got)
		}
	}
}

func TestParseGeofeed(t *testing.T) {
	feed := "\ufeff# comment\n" +
		"\n" +
		"192.0.2.0/24,RU,RU-SVE,Yekaterinburg,620000\n" +
		"\"2001:db8::/32\",\"ru\",\"ru-mow\",\"Moscow\"\n" +
		"198.51.100.7,DE,,,\n" +
		"198.51.100.129/25,FR\n" +
		"::ffff:203.0.113.0/120,NL,NL-NH,Amsterdam,\n" +
		"bogus,RU,,,\n" +
		"203.0.113.0/24,RUS,,,\n" +
		"fe80::1%eth0/128,RU,,,\n" +
		"10.0.0.0/8,,,,\n"
	entries, bad, err := ParseGeofeed(strings.NewReader(feed), 0)
	if err != nil {
		t.Fatal(err)
	}
	if bad != 3 || len(entries) != 6 {
		t.Fatalf("entries %d bad %d: %+v", len(entries), bad, entries)
	}
	want := []Entry{
		{P: netip.MustParsePrefix("192.0.2.0/24"), Country: "RU", Region: "RU-SVE", City: "Yekaterinburg"},
		{P: netip.MustParsePrefix("2001:db8::/32"), Country: "RU", Region: "RU-MOW", City: "Moscow"},
		{P: netip.MustParsePrefix("198.51.100.7/32"), Country: "DE"},
		{P: netip.MustParsePrefix("198.51.100.128/25"), Country: "FR"},
		{P: netip.MustParsePrefix("203.0.113.0/24"), Country: "NL", Region: "NL-NH", City: "Amsterdam"},
		{P: netip.MustParsePrefix("10.0.0.0/8")},
	}
	if !slices.Equal(entries, want) {
		t.Errorf("entries:\n%+v\nwant\n%+v", entries, want)
	}
	if entries, bad, _ := ParseGeofeed(strings.NewReader(feed), 2); len(entries) != 2 || bad != 7 {
		t.Errorf("limit: %d entries, %d bad", len(entries), bad)
	}
}

func testVocab() *Vocab {
	return NewVocab([]Name{
		{"Sverdlovsk Oblast", "SVE"}, {"Sverdlovsk", "SVE"}, {"Yekaterinburg", "SVE"}, {"Ekb", "SVE"},
		{"Nizhny Novgorod Oblast", "NIZ"}, {"Nizhny Novgorod", "NIZ"}, {"N Novgorod", "NIZ"},
		{"Novgorod Oblast", "NGR"}, {"Veliky Novgorod", "NGR"},
		{"Moscow", "MOW"}, {"Moscow Oblast", "MOS"}, {"Krasnogorsk", "MOS"},
		{"Tatarstan", "TA"}, {"Republic of Tatarstan", "TA"}, {"Kazan", "TA"},
		{"Altai Republic", "AL"}, {"Altai", "AL"}, {"Altai Krai", "ALT"}, {"Barnaul", "ALT"},
		{"Vladimir Oblast", "VLA"}, {"Vladimir", "VLA"},
		{"Troitsk", "MOW"}, {"Troitsk", "CHE"},
		{"Altai", ""}, {"Vladimir", ""},
	}, []string{"MOW"}, []string{"MOS"})
}

func TestVocabMatch(t *testing.T) {
	v := testVocab()
	cases := []struct {
		texts     []string
		includeHQ bool
		want      string
	}{
		{[]string{"PJSC Rostelecom", "Yekaterinburg branch"}, false, "SVE"},
		{[]string{"Nizhny Novgorod"}, false, "NIZ"},
		{[]string{"N.Novgorod city network"}, false, "NIZ"},
		{[]string{"Veliky Novgorod"}, false, "NGR"},
		{[]string{"Novgorod"}, false, ""},
		{[]string{"Sverdlovsk region"}, false, "SVE"},
		{[]string{"SVERDLOVSK-OBL"}, false, "SVE"},
		{[]string{"Tatarstan Republic"}, false, "TA"},
		{[]string{"Moscow, Russia"}, false, ""},
		{[]string{"Moscow, Russia"}, true, "MOW"},
		{[]string{"Moscow office", "Kazan network"}, false, "TA"},
		{[]string{"Moscow and Moscow Oblast"}, false, ""},
		{[]string{"Moscow and Moscow region"}, true, ""},
		{[]string{"Moscow Oblast"}, false, "MOS"},
		{[]string{"Kazan and Yekaterinburg"}, false, ""},
		{[]string{"Altai"}, false, ""},
		{[]string{"Altai Krai"}, false, "ALT"},
		{[]string{"Altai Republic"}, false, "AL"},
		{[]string{"Vladimir Ivanov"}, false, ""},
		{[]string{"Vladimir Oblast"}, false, "VLA"},
		{[]string{"MTS-EKB2-POOL"}, false, "SVE"},
		{[]string{"Kazanskiy"}, false, ""},
		{[]string{"Troitsk"}, false, ""},
		{[]string{"Ekaterinburg"}, false, ""},
		{nil, false, ""},
	}
	for _, c := range cases {
		got, name := v.Match(c.includeHQ, c.texts...)
		if got != c.want {
			t.Errorf("Match(%v, %q) = %q (%q), want %q", c.includeHQ, c.texts, got, name, c.want)
		}
	}
	if _, name := v.Match(false, "Rostelecom, Sverdlovsk region"); name != "sverdlovsk region" {
		t.Errorf("matched name %q", name)
	}
}

func TestWords(t *testing.T) {
	got := words("RU-MTS_NNov2016 St.-Petersburg Café 10G")
	want := []string{"ru", "mts", "nnov", "2016", "st", "petersburg", "caf", "10", "g"}
	if !slices.Equal(got, want) {
		t.Errorf("words = %q, want %q", got, want)
	}
}

func TestRank(t *testing.T) {
	r := func(s string) iprange.Range {
		x, err := iprange.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !(Rank(ViaGeofeed, r("5.0.0.0/8")) > Rank(ViaWhois, r("5.0.0.0/32"))) {
		t.Error("every geofeed hint must outrank every whois hint")
	}
	if !(Rank(ViaWhois, r("5.0.0.0/24")) > Rank(ViaWhois, r("5.0.0.0/16"))) {
		t.Error("smaller ranges must rank higher")
	}
	if Rank(ViaWhois, r("5.0.0.0-5.0.2.255")) != Rank(ViaWhois, r("5.0.0.0/23")) {
		t.Error("unaligned ranges rank by the largest power of two inside")
	}
	if Rank(ViaGeofeed, r("2001:db8::/48")) != 2000+48 {
		t.Errorf("IPv6 rank %d", Rank(ViaGeofeed, r("2001:db8::/48")))
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	split := `inetnum: 5.0.0.0 - 5.0.255.255
netname: RT-EKB
descr: Yekaterinburg branch
country: RU
geofeed: https://feeds.example/a.csv

inetnum: 5.0.16.0 - 5.0.16.255
netname: CUSTOMER
descr: Kazan office
country: RU
geofeed: https://feeds.example/a.csv

inetnum: 5.1.0.0 - 5.1.255.255
netname: MSK
descr: Moscow
country: RU
geofeed: https://feeds.example/missing.csv

inetnum: 6.0.0.0 - 6.0.255.255
netname: DE-NET
descr: Kazan
country: DE
geofeed: https://feeds.example/de.csv

inet6num: 2a00:1::/32
netname: V6
country: RU
geofeed: https://feeds.example/a.csv
`
	sp := filepath.Join(dir, "split")
	if err := os.WriteFile(sp, []byte(split), 0o644); err != nil {
		t.Fatal(err)
	}
	feed := "5.0.200.0/24,RU,RU-TA,,\n" + // inside the /16 although the /24 object starts later
		"5.0.16.0/25,RU,RU-SVE,,\n" +
		"5.0.17.0/24,RU,,Yekaterinburg,\n" + // city only
		"5.0.18.0/24,RU,RU-XX,Nowhere,\n" + // unknown region
		"5.0.19.0/24,UA,,,\n" + // another country: block
		"5.0.20.0/24,,,,\n" + // no country
		"5.9.0.0/24,RU,RU-SVE,,\n" + // outside every referencing object
		"2a00:1:2::/48,RU,RU-MOW,,\n" +
		"junk\n"
	fp := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(fp, []byte(feed), 0o644); err != nil {
		t.Fatal(err)
	}
	countries := []Country{{Code: "RU", Vocab: testVocab(), Regions: map[string]bool{"SVE": true, "TA": true, "MOW": true, "MOS": true}}}
	hints, st, err := Collect([]string{sp}, countries, func(u string) (string, bool) {
		if u == "https://feeds.example/a.csv" {
			return fp, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Objects != 5 || st.CountryObjects["RU"] != 4 || st.WhoisHints["RU"] != 2 || st.GeofeedURLs != 2 || st.GeofeedsRead != 1 ||
		st.Entries != 8 || st.Accepted["RU"] != 4 || st.CountryOnly != 1 || st.OtherCountry != 1 || st.NoCountry != 1 ||
		st.Outside != 1 || st.BadLines != 1 || st.UnmatchedRegions["RU"]["RU-XX"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
	type h struct {
		r, region, via string
		block          bool
	}
	var got []h
	for _, x := range hints {
		got = append(got, h{x.R.String(), x.Region, x.Via, x.Block})
	}
	want := []h{
		{"5.0.0.0-5.0.255.255", "SVE", ViaWhois, false},
		{"5.0.16.0-5.0.16.255", "TA", ViaWhois, false},
		{"5.0.200.0-5.0.200.255", "TA", ViaGeofeed, false},
		{"5.0.16.0-5.0.16.127", "SVE", ViaGeofeed, false},
		{"5.0.17.0-5.0.17.255", "SVE", ViaGeofeed, false},
		{"5.0.19.0-5.0.19.255", "", ViaGeofeed, true},
		{"2a00:1:2::-2a00:1:2:ffff:ffff:ffff:ffff:ffff", "MOW", ViaGeofeed, false},
	}
	if !slices.Equal(got, want) {
		t.Errorf("hints:\n%+v\nwant\n%+v", got, want)
	}
}
