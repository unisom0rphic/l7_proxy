package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Validator contract:
//
//	func (cfg *Config) Validate() []error
//
// Pure function, no I/O. Wire validateFunc in TestMain once implemented:
//
//	func TestMain(m *testing.M) {
//		validateFunc = (*Config).Validate
//		os.Exit(m.Run())
//	}
var validateFunc func(*Config) []error

func TestMain(m *testing.M) {
	validateFunc = (*Config).Validate
	os.Exit(m.Run())
}

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

func withMirror(cfg *Config, route int, upstream string, ratio float64) *Config {
	cfg.Routes[route].Mirror.Upstream = upstream
	cfg.Routes[route].Mirror.Ratio = ratio
	return cfg
}

func validate(t *testing.T, cfg *Config) []error {
	t.Helper()
	if validateFunc == nil {
		t.Skip("config.Validate not implemented yet")
	}
	return validateFunc(cfg)
}

func wantOK(t *testing.T, cfg *Config) {
	t.Helper()
	if errs := validate(t, cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %d: %v", len(errs), errs)
	}
}

func wantErr(t *testing.T, cfg *Config) []error {
	t.Helper()
	errs := validate(t, cfg)
	if len(errs) == 0 {
		t.Fatalf("expected validation error, got none")
	}
	return errs
}

func wantErrContains(t *testing.T, cfg *Config, substr string) {
	t.Helper()
	errs := wantErr(t, cfg)
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return
		}
	}
	t.Fatalf("no error contains %q; got: %v", substr, errs)
}

func TestValidate_AcceptsValid(t *testing.T) {
	wantOK(t, validConfig())
}

func TestValidate_AcceptsValidWithMirror(t *testing.T) {
	wantOK(t, withMirror(validConfig(), 0, "b", 0.5))
}

func TestValidate_AcceptsZeroRatio(t *testing.T) {
	wantOK(t, withMirror(validConfig(), 0, "b", 0))
}

func TestValidate_AcceptsRatioOne(t *testing.T) {
	wantOK(t, withMirror(validConfig(), 0, "b", 1.0))
}

func TestValidate_AcceptsEmptyMirror(t *testing.T) {
	wantOK(t, validConfig())
}

func TestValidate_AcceptsDifferentMethodsSamePath(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[1].Rule.PathPrefix = "/api"
	cfg.Routes[1].Rule.Method = "GET"
	wantOK(t, cfg)
}

func TestValidate_RejectsStructure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"route points to unknown upstream",
			func(c *Config) { c.Routes[0].Upstream = "ghost" }},
		{"route has empty upstream",
			func(c *Config) { c.Routes[0].Upstream = "" }},
		{"mirror points to unknown upstream",
			func(c *Config) { withMirror(c, 0, "ghost", 0.5) }},
		{"mirror has empty upstream with ratio set",
			func(c *Config) { withMirror(c, 0, "", 0.5) }},
		{"mirror references own upstream",
			func(c *Config) { withMirror(c, 0, "a", 0.5) }},
		{"upstream has empty name",
			func(c *Config) { c.Upstreams[0].Name = "" }},
		{"upstream name is whitespace",
			func(c *Config) { c.Upstreams[0].Name = "   " }},
		{"duplicate upstream names",
			func(c *Config) { c.Upstreams[1].Name = "a" }},
		{"upstream has empty host",
			func(c *Config) { c.Upstreams[0].Host = "" }},
		{"no upstreams",
			func(c *Config) { c.Upstreams = nil }},
		{"no routes",
			func(c *Config) { c.Routes = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			wantErr(t, cfg)
		})
	}
}

func TestValidate_RejectsBadHost(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"no scheme (url.Parse treats testnet as scheme)", "testnet:3000"},
		{"unsupported scheme", "ftp://srv-a:1111"},
		{"empty scheme", "://srv-a:1111"},
		{"scheme with empty host", "http://"},
		{"port zero", "http://srv-a:0"},
		{"port out of range", "http://srv-a:99999"},
		{"port not a number", "http://srv-a:abc"},
		{"port negative", "http://srv-a:-1"},
		{"with query", "http://srv-a:1111?x=1"},
		{"with fragment", "http://srv-a:1111#frag"},
		{"with path", "http://srv-a:1111/foo"},
		{"space in host", "http://srv a:1111"},
		{"tab in host", "http://srv\t-a:1111"},
		{"newline in host", "http://srv-a:1111\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Upstreams[0].Host = tc.host
			wantErr(t, cfg)
		})
	}
}

func TestValidate_AcceptsGoodHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"http with port", "http://srv-a:1111"},
		{"https with port", "https://srv-a:443"},
		{"http without port", "http://srv-a"},
		{"ipv4", "http://127.0.0.1:8080"},
		{"ipv6", "http://[::1]:8080"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Upstreams[0].Host = tc.host
			wantOK(t, cfg)
		})
	}
}

func TestValidate_RejectsBadRule(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"path without leading slash",
			func(c *Config) { c.Routes[0].Rule.Path = "api" }},
		{"prefix without leading slash",
			func(c *Config) { c.Routes[0].Rule.PathPrefix = "api" }},
		{"both path and prefix set",
			func(c *Config) {
				c.Routes[0].Rule.Path = "/api"
				c.Routes[0].Rule.PathPrefix = "/api"
			}},
		{"rewrite without leading slash",
			func(c *Config) { c.Routes[0].Rule.Rewrite = "api/v2" }},
		{"rewrite with scheme",
			func(c *Config) { c.Routes[0].Rule.Rewrite = "http://x/y" }},
		{"rewrite with host",
			func(c *Config) { c.Routes[0].Rule.Rewrite = "//x/y" }},
		{"rewrite without path or prefix",
			func(c *Config) {
				c.Routes[0].Rule.Path = ""
				c.Routes[0].Rule.PathPrefix = ""
				c.Routes[0].Rule.Rewrite = "/v2"
			}},
		{"no match condition",
			func(c *Config) {
				c.Routes[0].Rule.Path = ""
				c.Routes[0].Rule.PathPrefix = ""
			}},
		{"method not in HTTP methods",
			func(c *Config) { c.Routes[0].Rule.Method = "FOO" }},
		{"header with space",
			func(c *Config) { c.Routes[0].Rule.Header = "X Req Source" }},
		{"header with colon",
			func(c *Config) { c.Routes[0].Rule.Header = "X-Bad:Header" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			wantErr(t, cfg)
		})
	}
}

func TestValidate_AcceptsGoodMethods(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		t.Run(m, func(t *testing.T) {
			cfg := validConfig()
			cfg.Routes[0].Rule.Method = m
			wantOK(t, cfg)
		})
	}
}

func TestValidate_RejectsDuplicates(t *testing.T) {
	t.Run("full route duplicate", func(t *testing.T) {
		cfg := validConfig()
		cfg.Routes[1].Rule = Rule{PathPrefix: "/api"}
		wantErr(t, cfg)
	})
	t.Run("catch-all duplicate", func(t *testing.T) {
		cfg := validConfig()
		cfg.Routes[0].Rule = Rule{}
		cfg.Routes[1].Rule = Rule{}
		wantErr(t, cfg)
	})
	t.Run("duplicate upstream names", func(t *testing.T) {
		cfg := validConfig()
		cfg.Upstreams[1].Name = "a"
		wantErr(t, cfg)
	})
}

func TestValidate_RejectsBadTimeouts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"upstream negative timeout_ms",
			func(c *Config) { c.Upstreams[0].Timeout = -1 }},
		{"transport negative tcp",
			func(c *Config) { c.NetworkTimeoutsSec.Transport.TCP = -1 }},
		{"transport negative keep_alive",
			func(c *Config) { c.NetworkTimeoutsSec.Transport.KeepAlive = -1 }},
		{"transport negative tls",
			func(c *Config) { c.NetworkTimeoutsSec.Transport.TLS = -1 }},
		{"transport negative response_header",
			func(c *Config) { c.NetworkTimeoutsSec.Transport.ResponseHeader = -1 }},
		{"transport negative idle_conn",
			func(c *Config) { c.NetworkTimeoutsSec.Transport.IdleConn = -1 }},
		{"server negative context",
			func(c *Config) { c.NetworkTimeoutsSec.Server.Context = -1 }},
		{"server negative read_header",
			func(c *Config) { c.NetworkTimeoutsSec.Server.ReadHeader = -1 }},
		{"server negative idle",
			func(c *Config) { c.NetworkTimeoutsSec.Server.Idle = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			wantErr(t, cfg)
		})
	}
}

func TestValidate_RejectsBadRatio(t *testing.T) {
	tests := []struct {
		name  string
		ratio float64
	}{
		{"negative", -0.1},
		{"above one", 1.5},
		{"far above one", 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, withMirror(validConfig(), 0, "b", tc.ratio))
		})
	}
}

func TestValidate_RejectsDirectCycle(t *testing.T) {
	cfg := withMirror(validConfig(), 0, "b", 1.0)
	cfg = withMirror(cfg, 1, "a", 1.0)
	wantErr(t, cfg)
}

func TestValidate_RejectsSelfLoop(t *testing.T) {
	wantErr(t, withMirror(validConfig(), 0, "a", 1.0))
}

func TestValidate_RejectsLongCycle(t *testing.T) {
	cfg := validConfig()
	cfg.Upstreams = append(cfg.Upstreams,
		Upstream{Name: "c", Host: "http://srv-c:3333", Timeout: 300},
		Upstream{Name: "d", Host: "http://srv-d:4444", Timeout: 300},
	)
	cfg.Routes = append(cfg.Routes,
		Route{Rule: Rule{PathPrefix: "/c"}, Upstream: "c"},
		Route{Rule: Rule{PathPrefix: "/d"}, Upstream: "d"},
	)
	cfg.Routes[0].Mirror.Upstream, cfg.Routes[0].Mirror.Ratio = "b", 1.0
	cfg.Routes[1].Mirror.Upstream, cfg.Routes[1].Mirror.Ratio = "c", 1.0
	cfg.Routes[2].Mirror.Upstream, cfg.Routes[2].Mirror.Ratio = "d", 1.0
	cfg.Routes[3].Mirror.Upstream, cfg.Routes[3].Mirror.Ratio = "a", 1.0
	wantErr(t, cfg)
}

func TestValidate_RejectsDisconnectedCycle(t *testing.T) {
	// a, b clean; c <-> d is a separate component.
	cfg := validConfig()
	cfg.Upstreams = append(cfg.Upstreams,
		Upstream{Name: "c", Host: "http://srv-c:3333", Timeout: 300},
		Upstream{Name: "d", Host: "http://srv-d:4444", Timeout: 300},
	)
	cfg.Routes = append(cfg.Routes,
		Route{Rule: Rule{PathPrefix: "/c"}, Upstream: "c"},
		Route{Rule: Rule{PathPrefix: "/d"}, Upstream: "d"},
	)
	cfg.Routes[2].Mirror.Upstream, cfg.Routes[2].Mirror.Ratio = "d", 1.0
	cfg.Routes[3].Mirror.Upstream, cfg.Routes[3].Mirror.Ratio = "c", 1.0
	wantErr(t, cfg)
}

func TestValidate_AcceptsDiamond(t *testing.T) {
	// a -> b -> d, a -> c -> d.
	cfg := validConfig()
	cfg.Upstreams = append(cfg.Upstreams,
		Upstream{Name: "c", Host: "http://srv-c:3333", Timeout: 300},
		Upstream{Name: "d", Host: "http://srv-d:4444", Timeout: 300},
	)
	cfg.Routes = []Route{
		{Rule: Rule{PathPrefix: "/a"}, Upstream: "a"},
		{Rule: Rule{PathPrefix: "/b"}, Upstream: "b"},
		{Rule: Rule{PathPrefix: "/c"}, Upstream: "c"},
		{Rule: Rule{PathPrefix: "/d"}, Upstream: "d"},
	}
	cfg.Routes[0].Mirror.Upstream, cfg.Routes[0].Mirror.Ratio = "b", 1.0
	cfg.Routes[1].Mirror.Upstream, cfg.Routes[1].Mirror.Ratio = "d", 1.0
	cfg.Routes[2].Mirror.Upstream, cfg.Routes[2].Mirror.Ratio = "d", 1.0
	wantOK(t, cfg)
}

func TestValidate_ReportsAllErrors(t *testing.T) {
	cfg := validConfig()
	cfg.Upstreams[0].Name = ""
	cfg.Upstreams[1].Host = ""
	cfg.Routes[0].Upstream = "ghost"
	cfg.Routes[0].Mirror.Upstream = "ghost"
	cfg.Routes[0].Mirror.Ratio = 2

	errs := validate(t, cfg)
	if len(errs) < 3 {
		t.Fatalf("expected multiple errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_NormalizesMethodBeforeDedup(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Rule.Method = "GET"
	cfg.Routes[1].Rule.PathPrefix = "/api"
	cfg.Routes[1].Rule.Method = "get"
	wantErrContains(t, cfg, "lowercase HTTP method")
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
