# OpenIPGeo

[中文](README.md)

An open-source IP geolocation database that is safe to redistribute, rebuilt daily by GitHub Actions. It merges DB-IP's free city database, ASN data from iptoasn, and the network lists published by CDN and cloud providers into standard MMDB files, and corrects mainland China provinces using the ASNs of provincial carrier networks.

> **Status: in development, not released yet.** The file name prefix `OpenIPGeo` is configured in [`config.json`](config.json).

## Downloads

| File | Content |
|---|---|
| `OpenIPGeo-City.mmdb` | Full edition: GeoLite2-City compatible structure plus ASN and network flags |
| `OpenIPGeo-City-Lite.mmdb` | Map edition: country, rounded coordinates, accuracy radius and network flags |
| `OpenIPGeo-City-Lite.mmdb.gz` | The Lite edition, gzip-compressed; identical after decompression |

Stable URLs (available after the first release):

- `https://github.com/jinkela1145/open-IPGeo/releases/latest/download/OpenIPGeo-City.mmdb`
- `https://github.com/jinkela1145/open-IPGeo/releases/latest/download/OpenIPGeo-City-Lite.mmdb`
- `https://github.com/jinkela1145/open-IPGeo/releases/latest/download/OpenIPGeo-City-Lite.mmdb.gz`
- jsDelivr: `https://cdn.jsdelivr.net/gh/jinkela1145/open-IPGeo@release/OpenIPGeo-City-Lite.mmdb.gz`

jsDelivr only serves files up to 20 MB, so the CDN URL carries the compressed Lite edition; get the full edition and the uncompressed Lite edition from Releases. Every file comes with a `.sha256` file. `manifest.json` records the version and hash of every upstream file and per-layer statistics; `ACCURACY.md` reports coverage of the address space delegated to China.

## Sources and merge order

Lowest priority first; later layers override earlier ones:

1. **Base**: DB-IP IP to City Lite (CC BY 4.0).
2. **Mainland China provinces**: many provincial carrier networks announce their prefixes in the global routing table with their own AS numbers. DB-IP often places the national carriers' space in Beijing, where their headquarters are (Beijing holds over a quarter of DB-IP's Chinese IPv4 space in 2026). When a prefix's origin AS belongs to a provincial network ([`data/cn_asn_province.csv`](data/cn_asn_province.csv), a few hundred entries, rules at the top of the file) and DB-IP says Beijing or gives no province, the province is replaced (capital coordinates, `source` = `bgp-asn`). When DB-IP agrees, its city-level result is kept; when DB-IP names another province, DB-IP is kept too. Prefix-to-AS data comes from iptoasn (PDDL 1.0).
   **Russia** uses the same rules with Moscow city as the headquarters default ([`data/ru_admin.csv`](data/ru_admin.csv), [`data/ru_asn_region.csv`](data/ru_asn_region.csv)). DB-IP already places Russian regional operators well; most of its Moscow bias comes from national carriers whose ASNs do not say the region, so the effect is much smaller than in China (about 0.4 % of Russian IPv4 in the 2026-06 data). The layer only refines records DB-IP already places in RU and never changes countries; the region table follows ISO 3166-2:RU, and DB-IP's region is kept whenever it is not in the table.
3. **Network flags**: Cloudflare and Fastly ranges, AWS / Google Cloud / Azure / Oracle cloud ranges, public DNS ranges confirmed to be anycast ([`data/anycast_prefixes.csv`](data/anycast_prefixes.csv)) and CDN ASNs ([`data/anycast_asns.csv`](data/anycast_asns.csv)).
4. **Manual overrides**: [`data/overrides.csv`](data/overrides.csv), highest priority.

Licenses and check dates of every source are listed in [SOURCES.md](SOURCES.md). Reserved and private networks are excluded.

## Fields

### Full edition

Same structure as GeoLite2-City (example record):

```json
{
  "continent": {"code": "AS", "geoname_id": 6255147, "names": {"en": "Asia", "zh-CN": "亚洲"}},
  "country": {"iso_code": "CN", "geoname_id": 1814991, "names": {"en": "China", "zh-CN": "中国"}},
  "subdivisions": [{"iso_code": "JS", "geoname_id": 1806260, "names": {"en": "Jiangsu", "zh-CN": "江苏省"}}],
  "location": {"latitude": 32.06167, "longitude": 118.77778, "accuracy_radius": 250},
  "autonomous_system_number": 56046,
  "autonomous_system_organization": "CMNET-JIANGSU-AP China Mobile communications corporation",
  "source": "bgp-asn"
}
```

- `continent`, `country`, `subdivisions`, `city`, `location`: as in GeoLite2-City. Names always include `en`; China province corrections also include `zh-CN`. `location.time_zone` is only present when upstream provides it.
- `autonomous_system_number`, `autonomous_system_organization`: same names as GeoLite2-ASN.
- `network`: `anycast`, `cdn`, `cloud` (vendor code), `cloud_region` (vendor region code). False or empty keys are omitted.
- `source`: the layer that produced the location: `dbip`, `bgp-asn` or `override`.
- `accuracy_radius`: DB-IP Lite has no radius, so it is derived: 50 km with a city, 250 km with a subdivision only, 1000 km with a country only, 1000 km for anycast, and for province corrections the radius of a circle with the province's land area (50–750 km, see [`data/cn_admin.csv`](data/cn_admin.csv)).

### Lite edition

Designed for world maps; **the fields are stable**:

```json
{
  "country":  {"iso_code": "JP"},
  "location": {"latitude": 35.5, "longitude": 139.5, "accuracy_radius": 50},
  "network":  {"anycast": true, "cdn": true, "cloud": "aws"}
}
```

Coordinates are rounded to 0.5° (configurable), radii use the tiers 10 / 25 / 50 / 100 / 250 / 500 / 1000 km, false or empty `network` keys are omitted, and there are no names, ASNs or `cloud_region`. `cloud` codes: `aws`, `gcp`, `azure`, `oracle`.

Locations are aggregated to IPv4 /24 and IPv6 /40 blocks: when a block is split between several locations it gets the location that covers most of its addresses, and `accuracy_radius` is widened until it covers two thirds of the block (MaxMind defines the radius at 67 % confidence). Blocks whose parts differ in country or network flags, or that have gaps, are left as they are, so countries and network flags are exactly those of the full edition. Use the full edition when you need locations finer than /24 or /40. `ACCURACY.md` reports how much was merged in each build.

The `.gz` file can be opened in memory with the standard library: `gzip.NewReader`, `io.ReadAll`, then `maxminddb.OpenBytes(data)`.

## Reading the databases

Go: use [`maxminddb-golang`](https://github.com/oschwald/maxminddb-golang). `geoip2-golang` only accepts a fixed list of MaxMind and DB-IP database types and will refuse these files.

Python: the full edition's `database_type` contains `City`, so `geoip2.database.Reader(...).city(ip)` works; read the extra fields with the `maxminddb` package. See the Chinese README for code samples.

## Updates

Upstream files are checked every day at 02:17 UTC and a release (tagged by date, e.g. `2026.10.02`) is published when an input changed. The newest 30 releases are kept.

The Azure download link changes every week; the build reads the current one from Microsoft's download page. If any upstream file other than the DB-IP base fails to download, the copy cached within the last 14 days is used and an issue is opened; if the DB-IP base fails, that day's release is skipped.

## Limitations

- Coordinates of anycast addresses have no geographic meaning, hence the 1000 km radius and `network.anycast`.
- Mobile networks can usually only be located to a province, sometimes only to a country.
- DB-IP Lite is a free edition with limited accuracy. An accuracy benchmark will be added to `ACCURACY.md`.
- China IPv6: the province correction only covers prefixes announced by provincial ASNs; most China Telecom prefixes are announced by the national backbone AS4134.
- Russia: DB-IP places about a third of Russian IPv4 in Moscow; much of the national carriers' mobile space is really elsewhere, but ASNs cannot tell. Regional data from the RIPE database or operator geofeeds would help; the licensing question is open (see [`SOURCES.md`](SOURCES.md)).
- No street-level location, no proxy / VPN detection.

## Attribution (required)

Data: [CC BY 4.0](DATA_LICENSE.md). Code: [Apache-2.0](LICENSE). When you use the data:

- credit this project and mention that it contains data from DB-IP and GeoNames;
- **web applications** must also show `<a href='https://db-ip.com'>IP Geolocation by DB-IP</a>` on pages that display or use lookup results (required by DB-IP).

## Corrections

Send a pull request that edits [`data/overrides.csv`](data/overrides.csv). Every row needs evidence (a link or an explanation), preferably information published by the network owner itself. Never copy from GeoLite2, CZ88/QQWry, IPIP.net or other databases that forbid redistribution, and never use APNIC whois results (APNIC's terms forbid using whois data for IP geolocation).

## Building

Go 1.24+ and network access to the upstream sites: `go run ./cmd/egeo all` (output in `dist/`), `go test ./...` for the tests.

## Dependencies

mmdbwriter v1.2.0 (Apache-2.0 OR MIT), maxminddb-golang v2.1.1 (ISC), go4.org/netipx (BSD-3-Clause, indirect), golang.org/x/sys (BSD-3-Clause, indirect). GitHub Actions are pinned to commit SHAs. No GPL / LGPL / AGPL code.
