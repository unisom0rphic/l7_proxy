package routing

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/unisom0rphic/l7proxy/internal/config"
)

// Prevents DCE
var sinkRoute *config.Route

// linearPrefixIndex is the pre-trie scan: one HasPrefix per route, O(N) lookups.
type linearPrefixIndex struct{ routes []config.Route }

func (l *linearPrefixIndex) match(path string) (*config.Route, bool) {
	var best *config.Route
	for i := range l.routes {
		r := &l.routes[i]
		p := r.Rule.PathPrefix
		if !strings.HasPrefix(path, p) {
			continue
		}
		if best == nil || len(p) > len(best.Rule.PathPrefix) {
			best = r
		}
	}
	return best, best != nil
}

// benchRoutes returns n realistic nested API prefixes.
func benchRoutes(n int) []config.Route {
	resources := []string{"users", "orders", "invoices", "products", "payments", "sessions", "reports", "events", "carts", "shipments"}
	routes := make([]config.Route, 0, n)
	for i := 0; i < n; i++ {
		prefix := fmt.Sprintf("/api/v%d/%s/%d", i%3+1, resources[i%len(resources)], i)
		routes = append(routes, config.Route{
			Rule:     config.Rule{PathPrefix: prefix},
			Upstream: fmt.Sprintf("upstream-%d", i%8),
		})
	}
	return routes
}

func BenchmarkPrefixRouting(b *testing.B) {
	req := &http.Request{}
	for _, n := range []int{10, 100, 1000} {
		routes := benchRoutes(n)
		cfg := &config.Config{Routes: routes}
		idx := newRouteIndex(cfg)
		linear := &linearPrefixIndex{routes: routes}

		queries := make([]string, 0, 64)
		for i := 0; i < len(routes) && i < 64; i++ {
			queries = append(queries, routes[i].Rule.PathPrefix+"/item/42")
		}

		b.Run(fmt.Sprintf("trie/prefixes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if c, ok := idx.MatchPrefix(req, queries[i%len(queries)]); ok {
					sinkRoute = c.route
				}
			}
		})

		b.Run(fmt.Sprintf("linear/prefixes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if r, ok := linear.match(queries[i%len(queries)]); ok {
					sinkRoute = r
				}
			}
		})
	}
}
