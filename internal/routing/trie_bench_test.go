package routing

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Prevents DCE
var sinkInt int

// dataset returns n unique words of length 3..12 over 'a'..'z'.
// Fixed seed.
func dataset(n int) []string {
	r := rand.New(rand.NewSource(42))
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	words := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for len(words) < n {
		l := 3 + r.Intn(10)
		var b strings.Builder
		b.Grow(l)
		for i := 0; i < l; i++ {
			b.WriteByte(alpha[r.Intn(len(alpha))])
		}
		w := b.String()
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		words = append(words, w)
	}
	return words
}

// queries returns half existing words (hits) and half guaranteed-absent words (misses).
// Miss = word+"zzz"
func queries(words []string, n int) []string {
	r := rand.New(rand.NewSource(7))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		w := words[r.Intn(len(words))]
		if i%2 == 0 {
			out = append(out, w) // hit
		} else {
			out = append(out, w+"zzz") // miss
		}
	}
	return out
}

// prefixes returns half real prefixes of length pl (hits) and half the same
// prefix with the last byte replaced by 0xFF (misses). 0xFF never appears in
// dataset (only a-z), so a miss fails exactly at depth pl. Both sides traverse
// the same number of nodes
func prefixes(words []string, pl, n int) []string {
	r := rand.New(rand.NewSource(int64(pl)*1000 + 7))
	eligible := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) >= pl {
			eligible = append(eligible, w)
		}
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		w := eligible[r.Intn(len(eligible))]
		if i%2 == 0 {
			out = append(out, w[:pl]) // hit
		} else {
			p := []byte(w[:pl])
			p[pl-1] = 0xFF // miss at depth pl
			out = append(out, string(p))
		}
	}
	return out
}

// ---------- linear baseline ----------

type linear struct{ words []string }

func (l *linear) Search(w string) bool {
	for _, v := range l.words {
		if v == w {
			return true
		}
	}
	return false
}

func (l *linear) StartsWith(prefix string) bool {
	for _, v := range l.words {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// sliceSetInsert inserts w into the slice-set if not present.
// Equivalent of Trie.Insert: O(N) per call, O(N^2) to build.
func sliceSetInsert(s []string, w string) []string {
	for _, v := range s {
		if v == w {
			return s
		}
	}
	return append(s, w)
}

// ---------- Search ----------

func BenchmarkSearch(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		words := dataset(n)
		qs := queries(words, 1000)

		b.Run(fmt.Sprintf("Trie/N=%d", n), func(b *testing.B) {
			tr := newTrie()
			for _, w := range words {
				tr.Insert(w)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var hits int
			for i := 0; i < b.N; i++ {
				if tr.Search(qs[i%len(qs)]) {
					hits++
				}
			}
			sinkInt = hits
		})

		b.Run(fmt.Sprintf("Linear/N=%d", n), func(b *testing.B) {
			ls := &linear{words: words}
			b.ReportAllocs()
			b.ResetTimer()
			var hits int
			for i := 0; i < b.N; i++ {
				if ls.Search(qs[i%len(qs)]) {
					hits++
				}
			}
			sinkInt = hits
		})
	}
}

// ---------- StartsWith ----------

// One op = one StartsWith for a prefix of fixed length pl.
func BenchmarkStartsWith(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		words := dataset(n)
		tr := newTrie()
		for _, w := range words {
			tr.Insert(w)
		}
		ls := &linear{words: words}

		for _, pl := range []int{1, 3, 5, 8} {
			ps := prefixes(words, pl, 1000)
			label := fmt.Sprintf("N=%d/L=%d", n, pl)

			b.Run("Trie/"+label, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				var hits int
				for i := 0; i < b.N; i++ {
					if tr.StartsWith(ps[i%len(ps)]) {
						hits++
					}
				}
				sinkInt = hits
			})

			b.Run("Linear/"+label, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				var hits int
				for i := 0; i < b.N; i++ {
					if ls.StartsWith(ps[i%len(ps)]) {
						hits++
					}
				}
				sinkInt = hits
			})
		}
	}
}

// ---------- Insert ----------

// One op = "build a set from N words with dedup".
// N capped at 10k: sliceSetInsert is O(N^2), so at 100k one iteration takes ~50s.
func BenchmarkInsert(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		words := dataset(n)

		b.Run(fmt.Sprintf("Trie/N=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tr := newTrie()
				for _, w := range words {
					tr.Insert(w)
				}
				sinkInt = tr.CountWords()
			}
		})

		b.Run(fmt.Sprintf("SliceSet/N=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s := make([]string, 0, n)
				for _, w := range words {
					s = sliceSetInsert(s, w)
				}
				sinkInt = len(s)
			}
		})
	}
}
