package main

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

func TestDecideRoute(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.Upstream{
			{Name: "test", Host: "http://testnet:3000"},
		},
		Routes: []config.Route{
			{
				Rule: config.Rule{
					PathPrefix: "/api/test",
				},
				Upstream: "test",
			},
		},
	}
	r, _ := routing.NewRouter(cfg)

	tests := []struct {
		path     string
		expected string
	}{
		{"/api/test", "http://testnet:3000/api"},
	}

	for _, tc := range tests {
		req := &http.Request{URL: &url.URL{Path: tc.path}}
		result, _ := r.DecideRoute(req)
		if result.String() != tc.expected {
			t.Errorf("Input: %s, expected %q, got: %q\n", tc.path, tc.expected, result)
		}
	}
}
