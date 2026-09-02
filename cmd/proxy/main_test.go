package main

import "testing"

func TestDecideRoute(t *testing.T) {
	config := Config{
		Upstreams: []Upstream{
			{Name: "test", Host: "http://testnet:3000"},
		},
		Routes: []Route{
			{
				Rule: Rule{
					PathPrefix: "/api/test",
				},
				Upstream: "test",
			},
		},
	}
	tests := []struct {
		path     string
		expected string
	}{
		{"/api/test", "http://testnet:3000/api"},
	}

	for _, tc := range tests {
		result, _ := config.decideRoute(tc.path)
		if result.String() != tc.expected {
			t.Errorf("Input: %s, expected %q, got: %q\n", tc.path, tc.expected, result)
		}
	}
}
