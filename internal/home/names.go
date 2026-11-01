package home

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const maxNameLen = 80

// cleanText makes a name from the Miniserver or the overlay safe to show: no control or format
// characters, single spaces, at most maxNameLen runes.
func cleanText(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= maxNameLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// normalize folds a name for matching: lower case, no diacritics, words of letters and digits
// separated by single spaces.
func normalize(s string) string {
	// a transformer keeps state, so every call makes its own
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		folded = s
	}
	folded = strings.ToLower(folded)
	var b strings.Builder
	space := false
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	return b.String()
}

// slug is a normalized name joined with dashes, for generated ids.
func slug(s string) string {
	return strings.ReplaceAll(normalize(s), " ", "-")
}

// levenshtein is the edit distance between two strings, by runes.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// suggest ranks candidate labels by closeness to the query and returns up to n of the values.
func suggest(query string, cands map[string]string, n int) []string {
	q := normalize(query)
	type scored struct {
		value string
		score int
	}
	best := map[string]int{}
	for label, value := range cands {
		l := normalize(label)
		d := levenshtein(q, l)
		if q != "" && (strings.Contains(l, q) || strings.Contains(q, l)) {
			d = min(d, 1)
		}
		// allow distance up to about half the query length
		if d > max(2, len([]rune(q))/2) {
			continue
		}
		if cur, ok := best[value]; !ok || d < cur {
			best[value] = d
		}
	}
	list := make([]scored, 0, len(best))
	for v, d := range best {
		list = append(list, scored{v, d})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score < list[j].score
		}
		return list[i].value < list[j].value
	})
	out := []string{}
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, list[i].value)
	}
	return out
}
