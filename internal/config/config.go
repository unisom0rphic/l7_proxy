package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

type TransportTimeouts struct {
	TCP            int `yaml:"tcp"`
	KeepAlive      int `yaml:"keep_alive"`
	TLS            int `yaml:"tls"`
	ResponseHeader int `yaml:"response_header"`
	IdleConn       int `yaml:"idle_conn"`
}

type ServerTimeoutes struct {
	Context    int `yaml:"context"`
	ReadHeader int `yaml:"read_header"`
	Idle       int `yaml:"idle"`
}

type Upstream struct {
	Name    string `yaml:"name"`
	Host    string `yaml:"host"`
	Timeout int    `yaml:"timeout_ms"`
}

type Rule struct {
	PathPrefix string `yaml:"path_prefix"`
	Header     string `yaml:"header"`
	Path       string `yaml:"path"`
	Rewrite    string `yaml:"rewrite"`
	Method     string `yaml:"method"`
}

type Route struct {
	Rule     Rule   `yaml:"rules"`
	Upstream string `yaml:"upstream"`
	Mirror   struct {
		Upstream string  `yaml:"upstream"`
		Ratio    float64 `yaml:"ratio"`
	} `yaml:"mirror"`
}

type Config struct {
	NetworkTimeoutsSec struct {
		Transport TransportTimeouts `yaml:"transport"`
		Server    ServerTimeoutes   `yaml:"server"`
	} `yaml:"net_timeouts_s"`
	Upstreams []Upstream `yaml:"upstreams"`
	Routes    []Route    `yaml:"routes"`
}

var httpMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {},
	"DELETE": {}, "HEAD": {}, "OPTIONS": {}, "CONNECT": {}, "TRACE": {},
}

// Validate returns all semantic errors in cfg. Empty slice means valid.
func (cfg *Config) Validate() []error {
	if cfg == nil {
		return []error{errors.New("config is nil")}
	}

	var errs []error
	errs = append(errs, validateUpstreams(cfg.Upstreams)...)
	errs = append(errs, validateRoutes(cfg.Routes, cfg.Upstreams)...)
	errs = append(errs, validateTimeouts(cfg)...)
	errs = append(errs, validateMirrorCycles(cfg)...)
	return errs
}

func validateUpstreams(upstreams []Upstream) []error {
	var errs []error
	if len(upstreams) == 0 {
		return []error{errors.New("no upstreams defined")}
	}

	seen := make(map[string]struct{}, len(upstreams))
	for i, u := range upstreams {
		switch {
		case strings.TrimSpace(u.Name) == "":
			errs = append(errs, fmt.Errorf("upstreams[%d]: name is empty", i))
		default:
			if _, dup := seen[u.Name]; dup {
				errs = append(errs, fmt.Errorf("upstreams[%d]: duplicate name %q", i, u.Name))
			} else {
				seen[u.Name] = struct{}{}
			}
		}

		if err := validateUpstreamHost(u.Host); err != nil {
			errs = append(errs, fmt.Errorf("upstreams[%d] (%s): %w", i, u.Name, err))
		}

		if u.Timeout < 0 {
			errs = append(errs, fmt.Errorf("upstreams[%d] (%s): timeout_ms must be >= 0, got %d", i, u.Name, u.Timeout))
		}
	}
	return errs
}

func validateUpstreamHost(host string) error {
	if host == "" {
		return errors.New("host is empty")
	}
	if strings.ContainsAny(host, " \t\n\r") {
		return fmt.Errorf("host %q contains whitespace", host)
	}

	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("host %q: %w", host, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("host %q: scheme must be http or https", host)
	}
	if u.Host == "" {
		return fmt.Errorf("host %q: missing host", host)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("host %q: must not contain a path", host)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("host %q: must not contain a query", host)
	}
	if u.Fragment != "" {
		return fmt.Errorf("host %q: must not contain a fragment", host)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("host %q: invalid port", host)
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("host %q: port %d out of range", host, n)
		}
	}
	return nil
}

func validateRoutes(routes []Route, upstreams []Upstream) []error {
	if len(routes) == 0 {
		return []error{errors.New("no routes defined")}
	}

	names := make(map[string]struct{}, len(upstreams))
	for _, u := range upstreams {
		names[u.Name] = struct{}{}
	}

	var errs []error
	seen := make(map[string]int, len(routes))

	for i, r := range routes {
		if r.Upstream == "" {
			errs = append(errs, fmt.Errorf("routes[%d]: upstream is empty", i))
		} else if _, ok := names[r.Upstream]; !ok {
			errs = append(errs, fmt.Errorf("routes[%d]: unknown upstream %q", i, r.Upstream))
		}

		errs = append(errs, validateRule(i, r.Rule)...)

		key := dedupKey(r.Rule)
		if j, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf("routes[%d]: duplicate of routes[%d]", i, j))
		} else {
			seen[key] = i
		}

		errs = append(errs, validateMirror(i, r, names)...)
	}
	return errs
}

func validateRule(i int, r Rule) []error {
	var errs []error

	if r.Path != "" && r.PathPrefix != "" {
		errs = append(errs, fmt.Errorf("routes[%d]: both path and path_prefix are set", i))
	}
	if r.Path == "" && r.PathPrefix == "" {
		errs = append(errs, fmt.Errorf("routes[%d]: both path and path_prefix are empty", i))
	}
	if err := validatePathLike(i, "path", r.Path); err != nil {
		errs = append(errs, err)
	}
	if err := validatePathLike(i, "path_prefix", r.PathPrefix); err != nil {
		errs = append(errs, err)
	}

	if r.Rewrite != "" {
		if r.Path == "" && r.PathPrefix == "" {
			errs = append(errs, fmt.Errorf("routes[%d]: rewrite requires path or path_prefix", i))
		}
		if err := validateRewrite(i, r.Rewrite); err != nil {
			errs = append(errs, err)
		}
	}

	if r.Method != "" {
		if r.Method != strings.ToUpper(r.Method) {
			errs = append(errs, fmt.Errorf("routes[%d]: lowercase HTTP method %q", i, r.Method))
		}
		if _, ok := httpMethods[strings.ToUpper(r.Method)]; !ok {
			errs = append(errs, fmt.Errorf("routes[%d]: unknown HTTP method %q", i, r.Method))
		}
	}

	if r.Header != "" && !isValidHeaderName(r.Header) {
		errs = append(errs, fmt.Errorf("routes[%d]: invalid header name %q", i, r.Header))
	}

	return errs
}

func validatePathLike(i int, field, v string) error {
	if v == "" {
		return nil
	}
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("routes[%d]: %s %q must start with /", i, field, v)
	}
	if strings.ContainsAny(v, " \t\n\r?#") {
		return fmt.Errorf("routes[%d]: %s %q contains invalid characters", i, field, v)
	}
	return nil
}

func validateRewrite(i int, v string) error {
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("routes[%d]: rewrite %q must start with /", i, v)
	}
	if strings.HasPrefix(v, "//") {
		return fmt.Errorf("routes[%d]: rewrite %q must not include host", i, v)
	}
	if strings.ContainsAny(v, " \t\n\r?#") {
		return fmt.Errorf("routes[%d]: rewrite %q contains invalid characters", i, v)
	}
	return nil
}

func validateMirror(i int, r Route, names map[string]struct{}) []error {
	var errs []error
	if r.Mirror.Upstream == "" {
		if r.Mirror.Ratio != 0 {
			errs = append(errs, fmt.Errorf("routes[%d]: mirror ratio set but mirror upstream is empty", i))
		}
		return errs
	}

	if _, ok := names[r.Mirror.Upstream]; !ok {
		errs = append(errs, fmt.Errorf("routes[%d]: mirror points to unknown upstream %q", i, r.Mirror.Upstream))
	}
	if r.Mirror.Upstream == r.Upstream {
		errs = append(errs, fmt.Errorf("routes[%d]: mirror references own upstream %q", i, r.Upstream))
	}
	if r.Mirror.Ratio < 0 || r.Mirror.Ratio > 1 {
		errs = append(errs, fmt.Errorf("routes[%d]: mirror ratio %v out of [0,1]", i, r.Mirror.Ratio))
	}
	return errs
}

func dedupKey(r Rule) string {
	return strings.ToUpper(r.Method) + "|" + r.Path + "|" + r.PathPrefix + "|" + r.Header
}

func isValidHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !isTokenChar(c) {
			return false
		}
	}
	return true
}

// isTokenChar reports whether c is a valid RFC 7230 tchar.
func isTokenChar(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z',
		c >= 'A' && c <= 'Z',
		c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func validateTimeouts(cfg *Config) []error {
	var errs []error
	t := cfg.NetworkTimeoutsSec.Transport
	s := cfg.NetworkTimeoutsSec.Server

	if t.TCP < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.transport.tcp must be >= 0, got %d", t.TCP))
	}
	if t.KeepAlive < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.transport.keep_alive must be >= 0, got %d", t.KeepAlive))
	}
	if t.TLS < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.transport.tls must be >= 0, got %d", t.TLS))
	}
	if t.ResponseHeader < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.transport.response_header must be >= 0, got %d", t.ResponseHeader))
	}
	if t.IdleConn < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.transport.idle_conn must be >= 0, got %d", t.IdleConn))
	}
	if s.Context < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.server.context must be >= 0, got %d", s.Context))
	}
	if s.ReadHeader < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.server.read_header must be >= 0, got %d", s.ReadHeader))
	}
	if s.Idle < 0 {
		errs = append(errs, fmt.Errorf("net_timeouts_s.server.idle must be >= 0, got %d", s.Idle))
	}
	return errs
}

func validateMirrorCycles(cfg *Config) []error {
	names := make(map[string]struct{}, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		names[u.Name] = struct{}{}
	}

	adj := make(map[string][]string, len(cfg.Upstreams))
	seen := make(map[[2]string]struct{})
	for _, r := range cfg.Routes {
		m := r.Mirror.Upstream
		if m == "" || m == r.Upstream {
			continue
		}
		if _, ok := names[r.Upstream]; !ok {
			continue
		}
		if _, ok := names[m]; !ok {
			continue
		}
		key := [2]string{r.Upstream, m}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		adj[r.Upstream] = append(adj[r.Upstream], m)
	}

	cycle := findCycle(adj)
	if cycle == nil {
		return nil
	}
	return []error{fmt.Errorf("mirror cycle detected: %s", strings.Join(cycle, " -> "))}
}

// findCycle runs a three-color DFS over adj and returns the first cycle found
// as a closed path (e.g. ["a","b","c","a"]), or nil if the graph is acyclic.
func findCycle(adj map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	state := make(map[string]int, len(adj))
	var stack []string

	var dfs func(string) []string
	dfs = func(n string) []string {
		state[n] = gray
		stack = append(stack, n)

		for _, m := range adj[n] {
			switch state[m] {
			case gray:
				for i, s := range stack {
					if s == m {
						cycle := append([]string{}, stack[i:]...)
						return append(cycle, m)
					}
				}
			case white:
				if c := dfs(m); c != nil {
					return c
				}
			}
		}

		stack = stack[:len(stack)-1]
		state[n] = black
		return nil
	}

	for n := range adj {
		if state[n] == white {
			if c := dfs(n); c != nil {
				return c
			}
		}
	}
	return nil
}

func ParseConfig(configPath string) (*Config, error) {
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading config (%s): %v\n", configPath, err)
	}

	var config Config
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		return nil, fmt.Errorf("failed yaml umarshal (%s): %v\n", configPath, err)
	}

	return &config, nil
}
