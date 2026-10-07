package config

// Data files of the regional layer.
const (
	FileCNAdmin = "cn_admin.csv"
	FileCNASN   = "cn_asn_province.csv"
	FileRUAdmin = "ru_admin.csv"
	FileRUASN   = "ru_asn_region.csv"
	FileRUNames = "ru_place_names.csv"
)

// RegionTable describes the curated tables of one country covered by the
// regional layer.
type RegionTable struct {
	Country   string // ISO 3166-1 code
	Lang      string // MMDB language key of the local names
	Col       string // suffix of the local-language columns in the admin table
	AdminFile string
	ASNFile   string
	ISOColumn string // region column of the ASN table
	// CarrierHQ are the subdivisions where the national carriers have their
	// headquarters and where DB-IP puts much of their space by default; only
	// there (or where DB-IP gives no subdivision) does an AS override DB-IP.
	CarrierHQ []string
	// HQNeighbors surround a headquarters region; RIPE object text that
	// names both ("Moscow and Moscow Oblast") is ambiguous.
	HQNeighbors []string
	// RIPE enables the RIPE Database layer (operators' geofeeds and the
	// place names in inetnum objects) for this country. PlaceNamesFile adds
	// spellings and city names to those of the admin table.
	RIPE           bool
	PlaceNamesFile string
	// Manifest layer counters.
	LayerRegions, LayerASN string
}

// RegionTables lists the countries of the regional layer.
var RegionTables = []RegionTable{
	{Country: "CN", Lang: "zh-CN", Col: "zh", AdminFile: FileCNAdmin, ASNFile: FileCNASN, ISOColumn: "province_iso",
		CarrierHQ: []string{"BJ"}, LayerRegions: "cn_provinces", LayerASN: "cn_asn_province"},
	{Country: "RU", Lang: "ru", Col: "ru", AdminFile: FileRUAdmin, ASNFile: FileRUASN, ISOColumn: "region_iso",
		CarrierHQ: []string{"MOW"}, HQNeighbors: []string{"MOS"}, RIPE: true, PlaceNamesFile: FileRUNames,
		LayerRegions: "ru_regions", LayerASN: "ru_asn_region"},
}
