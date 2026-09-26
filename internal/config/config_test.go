package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Validator contract under test:
//
//	func Validate(cfg *Config) error
//
// Pure function, no I/O, only internal consistency.
// validateFunc is the injection point. Validate is not implemented yet, so
// validation tests skip with a clear reason until then.
var validateFunc func(*Config) error

func validConfig() *Config {
	return &Config{
		Upstreams: []Upstream{
			{Name: "a", Host: "http://srv-a:1111", Timeout: 300},
			{Name: "b", Host: "http://srv-b:2222", Timeout: 300},
		},
		Routes: []Route{
			{Rule: Rule{PathPrefix: "/api"}, Upstream: "a"},
			{Rule: Rule{PathPrefix: "/api/v2", Method: "POST", Header: "X-Req-Source"}, Upstream: "b"},
		},
	}
}

func TestValidate_AcceptsValid(t *testing.T) {
	if validateFunc == nil {
		t.Skip("config.Validate not implemented yet — wire it into validateFunc")
	}
	if err := validateFunc(validConfig()); err != nil {
		t.Fatalf("valid config must not error: %v", err)
	}

	cfg := validConfig()
	cfg.Routes[0].Mirror.Upstream = "b"
	cfg.Routes[0].Mirror.Ratio = 0.5
	if err := validateFunc(cfg); err != nil {
		t.Fatalf("valid config with mirror must not error: %v", err)
	}
}

func TestValidate_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"route -> unknown upstream", func(c *Config) { c.Routes[0].Upstream = "ghost" }},
		{"mirror -> unknown upstream", func(c *Config) {
			c.Routes[0].Mirror.Upstream = "ghost"
			c.Routes[0].Mirror.Ratio = 0.5
		}},
		{"route -> empty upstream", func(c *Config) { c.Routes[0].Upstream = "" }},

		{"upstream -> empty name", func(c *Config) { c.Upstreams[0].Name = "" }},
		{"upstream -> empty host", func(c *Config) { c.Upstreams[0].Host = "" }},
		// url.Parse("testnet:3000") yields scheme="testnet".
		// Validator must require http/https explicitly.
		{"upstream -> host without scheme", func(c *Config) { c.Upstreams[0].Host = "testnet:3000" }},
		{"upstream -> unsupported scheme", func(c *Config) { c.Upstreams[0].Host = "ftp://srv-a:1111" }},
		{"upstream -> negative timeout_ms", func(c *Config) { c.Upstreams[0].Timeout = -1 }},

		// Assumption: empty prefix+path is an error. If empty prefix means
		// catch-all by design, skip this case.
		{"route -> no match condition", func(c *Config) {
			c.Routes[0].Rule.PathPrefix = ""
			c.Routes[0].Rule.Path = ""
		}},

		{"duplicate upstream names", func(c *Config) { c.Upstreams[1].Name = "a" }},

		// Assumption: a full duplicate route is an error, not a warning.
		{"duplicate routes", func(c *Config) { c.Routes[1].Rule = Rule{PathPrefix: "/api"} }},

		// Assumption: ratio is in [0,1]. If percentages or unbounded — skip.
		{"mirror ratio out of range", func(c *Config) {
			c.Routes[0].Mirror.Upstream = "b"
			c.Routes[0].Mirror.Ratio = 1.5
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			if validateFunc == nil {
				t.Skip("config.Validate not implemented yet — wire it into validateFunc")
			}
			if err := validateFunc(cfg); err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

func writeTmpConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseConfig_MissingFile(t *testing.T) {
	if _, err := ParseConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected error reading a missing file")
	}
}

func TestParseConfig_BrokenYAML(t *testing.T) {
	if _, err := ParseConfig(writeTmpConfig(t, "upstreams: [oops")); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestParseConfig_Fields(t *testing.T) {
	text := `net_timeouts_s:
  transport: {tcp: 5, keep_alive: 5, tls: 5, response_header: 7, idle_conn: 30}
  server: {context: 10, read_header: 5, idle: 30}
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout_ms: 300
routes:
  - rules:
      path_prefix: /v1
      method: POST
      header: X-Req-Source
      rewrite: /v2
    upstream: a
`
	cfg, err := ParseConfig(writeTmpConfig(t, text))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NetworkTimeoutsSec.Transport.ResponseHeader != 7 {
		t.Errorf("transport.response_header: want 7, got %d", cfg.NetworkTimeoutsSec.Transport.ResponseHeader)
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("upstreams: want 1, got %d", len(cfg.Upstreams))
	}
	u := cfg.Upstreams[0]
	if u.Name != "a" || u.Host != "http://srv-a:1111" || u.Timeout != 300 {
		t.Errorf("upstream: got %+v", u)
	}
	rt := cfg.Routes[0]
	if rt.Rule.PathPrefix != "/v1" || rt.Rule.Method != "POST" ||
		rt.Rule.Header != "X-Req-Source" || rt.Rule.Rewrite != "/v2" {
		t.Errorf("rule: got %+v", rt.Rule)
	}
	if rt.Upstream != "a" {
		t.Errorf("route.upstream: want a, got %q", rt.Upstream)
	}
}
