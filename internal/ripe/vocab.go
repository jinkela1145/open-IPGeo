package ripe

import (
	"slices"
	"strings"
)

// Name is a place name and the region it stands for. An empty Region marks a
// name that must never produce a hint (an ambiguous word or a personal name).
type Name struct {
	Text   string
	Region string
}

// Vocab finds the region named in the free text of RIPE objects (netname,
// descr) and in the region and city fields of geofeeds. Matching works on
// whole words, longest name first, so "Nizhny Novgorod" never matches the
// "Novgorod" of another region.
type Vocab struct {
	seqs      map[string]string // space-joined lower-case words -> region; "" = ignore
	maxLen    int
	hq        map[string]bool
	neighbors map[string]bool
}

// kindWords are the generic words that end region names; a name ending in
// one of them is also accepted with the other words of its group.
var kindWords = [][]string{
	{"oblast", "region", "obl"},
	{"krai", "kray", "territory"},
	{"republic", "respublika"},
	{"okrug", "district"},
}

// NewVocab builds a vocabulary. hq are the regions of the carriers'
// headquarters: whois text names them mostly as a postal address, so Match
// leaves them out unless asked to include them. neighbors are regions that
// surround a headquarters region ("Moscow and Moscow Oblast"): text naming
// both is ambiguous.
func NewVocab(names []Name, hq, neighbors []string) *Vocab {
	v := &Vocab{seqs: map[string]string{}, hq: map[string]bool{}, neighbors: map[string]bool{}}
	for _, h := range hq {
		v.hq[h] = true
	}
	for _, n := range neighbors {
		v.neighbors[n] = true
	}
	ignored := map[string]bool{}
	add := func(words []string, region string) {
		if len(words) == 0 {
			return
		}
		k := strings.Join(words, " ")
		if ignored[k] {
			return
		}
		if prev, ok := v.seqs[k]; ok && prev != region {
			v.seqs[k] = "" // two regions use the same name: ambiguous
			return
		}
		v.seqs[k] = region
		v.maxLen = max(v.maxLen, len(words))
	}
	for _, n := range names {
		if n.Region == "" {
			if w := words(n.Text); len(w) > 0 {
				k := strings.Join(w, " ")
				ignored[k] = true
				v.seqs[k] = ""
				v.maxLen = max(v.maxLen, len(w))
			}
		}
	}
	for _, n := range names {
		if n.Region == "" {
			continue
		}
		for _, w := range variants(words(n.Text)) {
			add(w, n.Region)
		}
	}
	return v
}

// variants returns the name with its generic ending exchanged for the other
// words of the same group, and "republic of X" written as "X republic".
func variants(w []string) [][]string {
	out := [][]string{w}
	if len(w) >= 3 && w[0] == "republic" && w[1] == "of" {
		out = append(out, append(slices.Clone(w[2:]), "republic"))
	}
	for _, base := range slices.Clone(out) {
		if len(base) < 2 {
			continue
		}
		last := base[len(base)-1]
		for _, g := range kindWords {
			if !slices.Contains(g, last) {
				continue
			}
			for _, alt := range g {
				if alt != last {
					out = append(out, append(slices.Clone(base[:len(base)-1]), words(alt)...))
				}
			}
		}
	}
	return out
}

// Match returns the single region named in the texts and the name that
// matched. When names of several regions appear, or none, it returns "".
// Unless includeHQ is set, names of headquarters regions are ignored.
func (v *Vocab) Match(includeHQ bool, texts ...string) (region, name string) {
	found, sawHQ := "", false
	defer func() {
		if sawHQ && !includeHQ && v.neighbors[region] {
			region, name = "", ""
		}
	}()
	var buf []byte
	for _, t := range texts {
		w := words(t)
		for i := 0; i < len(w); {
			n := min(v.maxLen, len(w)-i)
			matched := 0
			for ; n > 0; n-- {
				buf = buf[:0]
				for j, x := range w[i : i+n] {
					if j > 0 {
						buf = append(buf, ' ')
					}
					buf = append(buf, x...)
				}
				r, ok := v.seqs[string(buf)]
				if !ok {
					continue
				}
				k := string(buf)
				matched = n
				if v.hq[r] {
					sawHQ = true
				}
				if r == "" || (v.hq[r] && !includeHQ) {
					break
				}
				if found != "" && found != r {
					return "", ""
				}
				if found == "" {
					found, name = r, k
				}
				break
			}
			if matched == 0 {
				matched = 1
			}
			i += matched
		}
	}
	return found, name
}

// words splits text into lower-case ASCII words. Anything else separates
// words, and so does a change between letters and digits ("SPB2" is "spb 2").
func words(s string) []string {
	var out []string
	start := -1
	kind := 0 // 1 letter, 2 digit
	flush := func(end int) {
		if start >= 0 {
			out = append(out, strings.ToLower(s[start:end]))
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		k := 0
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			k = 1
		case c >= '0' && c <= '9':
			k = 2
		}
		if k == 0 {
			flush(i)
			continue
		}
		if start >= 0 && k != kind {
			flush(i)
		}
		if start < 0 {
			start, kind = i, k
		}
	}
	flush(len(s))
	return out
}
