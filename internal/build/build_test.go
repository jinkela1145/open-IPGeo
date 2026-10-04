package build

import (
	"math"
	"testing"

	"github.com/jinkela1145/open-IPGeo/internal/sources"
)

func TestRadiusTier(t *testing.T) {
	tiers := []uint16{10, 25, 50, 100, 250, 500, 1000}
	for in, want := range map[uint16]uint16{1: 10, 10: 10, 11: 25, 50: 50, 51: 100, 250: 250, 300: 500, 1000: 1000, 5000: 1000} {
		if got := radiusTier(in, tiers); got != want {
			t.Errorf("radiusTier(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRounding(t *testing.T) {
	cases := []struct {
		v    float64
		want float64
	}{{35.69, 35.5}, {35.76, 36}, {-0.2, 0}, {-33.87, -34}, {139.69, 139.5}, {-122.42, -122.5}, {180, 180}}
	for _, c := range cases {
		got := cleanZero(float64(roundTo(c.v, 0.5)) * 0.5)
		if got != c.want || math.Signbit(got) != math.Signbit(c.want) {
			t.Errorf("round(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestFindRegion(t *testing.T) {
	r := &resolver{regions: []sources.Region{
		{Country: "CN", Lang: "zh-CN", ISO: "NM", NameLocal: "内蒙古自治区", NameEN: "Inner Mongolia", DBIPNames: []string{"Nei Mongol", "Inner Mongolia Autonomous Region"}},
		{Country: "CN", Lang: "zh-CN", ISO: "GD", NameLocal: "广东省", NameEN: "Guangdong"},
		{Country: "CN", Lang: "zh-CN", ISO: "BJ", NameLocal: "北京市", NameEN: "Beijing"},
		{Country: "RU", Lang: "ru", ISO: "SVE", NameLocal: "Свердловская область", NameEN: "Sverdlovsk Oblast", DBIPNames: []string{"Sverdlovsk"}},
		{Country: "RU", Lang: "ru", ISO: "MOW", NameLocal: "Москва", NameEN: "Moscow"},
	}, byCountry: map[string][]int16{"CN": {0, 1, 2}, "RU": {3, 4}}}
	for _, c := range []struct {
		country, name string
		want          int16
	}{
		{"CN", "inner mongolia", 0}, {"CN", "Nei Mongol", 0}, {"CN", "内蒙古", 0}, {"CN", "内蒙古自治区", 0}, {"CN", "NM", 0},
		{"CN", "广东", 1}, {"CN", "广东省", 1}, {"CN", "GUANGDONG", 1}, {"CN", "北京", 2}, {"CN", "Beijing", 2},
		{"CN", "", -1}, {"CN", "Guangzhou", -1}, {"CN", "广州市", -1},
		{"RU", "Sverdlovsk", 3}, {"RU", "SVERDLOVSK OBLAST", 3}, {"RU", "Свердловская область", 3}, {"RU", "MOW", 4},
		{"RU", "Yekaterinburg", -1}, {"RU", "Beijing", -1}, {"CN", "Moscow", -1},
	} {
		if got := r.findRegion(c.country, c.name); got != c.want {
			t.Errorf("findRegion(%s, %q) = %d, want %d", c.country, c.name, got, c.want)
		}
	}
}

func TestNetFlagsMerge(t *testing.T) {
	a := sources.NetFlags{Cloud: "aws", Region: "us-east-1"}
	b := sources.NetFlags{CDN: true}
	if got := a.Merge(b); got != (sources.NetFlags{CDN: true, Cloud: "aws", Region: "us-east-1"}) {
		t.Errorf("merge = %+v", got)
	}
	if got := b.Merge(sources.NetFlags{Anycast: true, Region: "eu-west-1"}); !got.Anycast || !got.CDN || got.Region != "eu-west-1" {
		t.Errorf("merge = %+v", got)
	}
}
