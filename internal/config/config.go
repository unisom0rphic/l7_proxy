package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

type TransportTimeouts struct {
	TCP            int `yaml:"tcp"`
	KeepAlive      int `yaml:"keep-alive"`
	TLS            int `yaml:"tls"`
	ResponseHeader int `yaml:"response-header"`
	IdleConn       int `yaml:"idle-conn"`
}

type ServerTimeouts struct {
	Context    int `yaml:"context"`
	ReadHeader int `yaml:"read-header"`
	Idle       int `yaml:"idle"`
}

type Upstream struct {
	Name    string `yaml:"name"`
	Host    string `yaml:"host"`
	Timeout int    `yaml:"timeout-ms"`
}

type HeaderOp string

const (
	HeaderPresent  HeaderOp = "present"
	HeaderEquals   HeaderOp = "equals"
	HeaderPrefix   HeaderOp = "prefix"
	HeaderContains HeaderOp = "contains"
)

type HeaderMatch struct {
	Name  string   `yaml:"name"`
	Value string   `yaml:"value,omitempty"`
	Op    HeaderOp `yaml:"op,omitempty"`
}

// EffectiveOp resolves the shorthand: empty Op + empty Value = present,
// empty Op + Value set = equals, explicit Op wins.
func (h HeaderMatch) EffectiveOp() HeaderOp {
	if h.Op != "" {
		return h.Op
	}
	if h.Value == "" {
		return HeaderPresent
	}
	return HeaderEquals
}

type Rule struct {
	PathPrefix  string        `yaml:"path-prefix"`
	Path        string        `yaml:"path"`
	Rewrite     string        `yaml:"rewrite"`
	Method      string        `yaml:"method"`
	Headers     []HeaderMatch `yaml:"headers,omitempty"`
	Hosts       []string      `yaml:"hosts,omitempty"`
	ClientCIDRs []string      `yaml:"client-cidrs,omitempty"`
}

type Mirror struct {
	Upstream string  `yaml:"upstream"`
	Ratio    float64 `yaml:"ratio"`
}

type Route struct {
	Rule     Rule     `yaml:"rules"`
	Upstream string   `yaml:"upstream"`
	Mirrors  []Mirror `yaml:"mirrors"`
}

type ConnectionPool struct {
	MaxIdleConns        int `yaml:"max-idle-conns"`
	MaxIdleConnsPerHost int `yaml:"max-idle-conns-per-host"`
}

type MirroringConfig struct {
	Workers   int `yaml:"workers"`
	QueueSize int `yaml:"queue-size"`
}

type InfrastructureConfig struct {
	NetworkTimeoutsSec struct {
		Transport TransportTimeouts `yaml:"transport"`
		Server    ServerTimeouts   `yaml:"server"`
	} `yaml:"net-timeouts-s"`
	ConnectionPool ConnectionPool `yaml:"connection-pool"`
	Mirroring      MirroringConfig `yaml:"mirroring"`
	Port           string          `yaml:"port"`
}

type Config struct {
	Upstreams  []Upstream          `yaml:"upstreams"`
	Routes     []Route             `yaml:"routes"`
	NameToHost map[string]*url.URL `yaml:"-"`
}

var httpMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {},
	"DELETE": {}, "HEAD": {}, "OPTIONS": {}, "CONNECT": {}, "TRACE": {},
}

// Maps upstream names to their hosts (e.g users-prod -> http://users-prod:3001)
func (cfg *Config) mapNamesToHosts() error {
	nameToHost := make(map[string]*url.URL)
	for _, upstream := range cfg.Upstreams {
		name := upstream.Name
		host := upstream.Host

		u, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("error mapping upstream %s to host %s, %w", name, host, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("error parsing config url for %s: incorrect scheme: %s", name, host)
		}
		nameToHost[name] = u
	}
	cfg.NameToHost = nameToHost

	return nil
}

// Validate returns all semantic errors in cfg. Empty slice means valid.
func (cfg *Config) Validate() []error {
	if cfg == nil {
		return []error{errors.New("config is nil")}
	}

	var errs []error
	errs = append(errs, validateUpstreams(cfg.Upstreams)...)
	errs = append(errs, validateRoutes(cfg.Routes, cfg.Upstreams)...)
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
			errs = append(errs, fmt.Errorf("upstreams[%d] (%s): timeout-ms must be >= 0, got %d", i, u.Name, u.Timeout))
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
		errs = append(errs, fmt.Errorf("routes[%d]: both path and path-prefix are set", i))
	}
	if r.Path == "" && r.PathPrefix == "" {
		errs = append(errs, fmt.Errorf("routes[%d]: both path and path-prefix are empty", i))
	}
	if err := validatePathLike(i, "path", r.Path); err != nil {
		errs = append(errs, err)
	}
	if err := validatePathLike(i, "path-prefix", r.PathPrefix); err != nil {
		errs = append(errs, err)
	}

	if r.Rewrite != "" {
		if r.Path == "" && r.PathPrefix == "" {
			errs = append(errs, fmt.Errorf("routes[%d]: rewrite requires path or path-prefix", i))
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

	errs = append(errs, validateHeaders(i, r.Headers)...)
	errs = append(errs, validateHosts(i, r.Hosts)...)
	errs = append(errs, validateClientCIDRs(i, r.ClientCIDRs)...)

	return errs
}

// One route = AND across every header rule (and across method, hosts, client-cidrs).
// A request matches only if every entry here matches.
//
// Under AND, two rules on the same header name are almost always a config
// author's mistake: they mean OR ("X-Foo is a or b"), which this schema cannot
// express in a single route. Reject duplicates and point at the alternative.
func validateHeaders(i int, hs []HeaderMatch) []error {
	var errs []error
	seen := make(map[string]int, len(hs))

	for j, h := range hs {
		if !isValidHeaderName(h.Name) {
			errs = append(errs, fmt.Errorf("routes[%d].headers[%d]: invalid header name %q", i, j, h.Name))
			continue
		}

		key := strings.ToLower(h.Name)
		if prev, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf(
				"routes[%d].headers[%d]: duplicate header %q (also at [%d]); "+
					"headers on one route are ANDed. split into separate routes for OR",
				i, j, h.Name, prev))
			continue
		}
		seen[key] = j

		switch h.EffectiveOp() {
		case HeaderPresent:
			if h.Value != "" {
				errs = append(errs, fmt.Errorf(
					"routes[%d].headers[%d]: op=present must not set value", i, j))
			}
		case HeaderEquals, HeaderPrefix, HeaderContains:
			if h.Value == "" {
				errs = append(errs, fmt.Errorf(
					"routes[%d].headers[%d]: op=%s requires value", i, j, h.EffectiveOp()))
			}
		default:
			errs = append(errs, fmt.Errorf(
				"routes[%d].headers[%d]: unknown op %q", i, j, h.Op))
		}
	}
	return errs
}

func validateHosts(i int, hosts []string) []error {
	var errs []error
	for j, h := range hosts {
		if err := validateHostPattern(h); err != nil {
			errs = append(errs, fmt.Errorf("routes[%d].hosts[%d]: %w", i, j, err))
		}
	}
	return errs
}

func validateHostPattern(h string) error {
	if h == "" {
		return errors.New("host is empty")
	}
	if strings.ContainsAny(h, " \t\n\r") {
		return fmt.Errorf("host %q contains whitespace", h)
	}
	if strings.ContainsAny(h, "/?#") {
		return fmt.Errorf("host %q must not contain /, ?, or #", h)
	}
	if strings.Contains(h, ":") {
		// r.Host can carry a port, but matching becomes ambiguous with wildcards.
		// Reject ports here and let the router compare against r.Host as-is.
		return fmt.Errorf("host %q must not contain a port", h)
	}
	if strings.Contains(h, "*") {
		if !strings.HasPrefix(h, "*.") || strings.Count(h, "*") != 1 {
			return fmt.Errorf("host %q: only a single leading wildcard (*.example.com) is supported", h)
		}
	}
	return nil
}

func validateClientCIDRs(i int, cidrs []string) []error {
	var errs []error
	for j, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			errs = append(errs, fmt.Errorf("routes[%d].client-cidrs[%d]: %w", i, j, err))
		}
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
	for _, mirror := range r.Mirrors {
		if mirror.Upstream == "" {
			if mirror.Ratio != 0 {
				errs = append(errs, fmt.Errorf("routes[%d]: mirror ratio set but mirror upstream is empty", i))
			}
			continue
		}

		if _, ok := names[mirror.Upstream]; !ok {
			errs = append(errs, fmt.Errorf("routes[%d]: mirror points to unknown upstream %q", i, mirror.Upstream))
		}
		if mirror.Upstream == r.Upstream {
			errs = append(errs, fmt.Errorf("routes[%d]: mirror references own upstream %q", i, r.Upstream))
		}
		if mirror.Ratio < 0 || mirror.Ratio > 1 {
			errs = append(errs, fmt.Errorf("routes[%d]: mirror ratio %v out of [0,1]", i, mirror.Ratio))
		}
	}

	return errs
}

func dedupKey(r Rule) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(r.Method))
	b.WriteByte('|')
	b.WriteString(r.Path)
	b.WriteByte('|')
	b.WriteString(r.PathPrefix)

	for _, h := range r.Headers {
		b.WriteByte('|')
		b.WriteString(strings.ToLower(h.Name))
		b.WriteByte('=')
		b.WriteString(string(h.EffectiveOp()))
		b.WriteByte(':')
		b.WriteString(h.Value)
	}
	for _, h := range r.Hosts {
		b.WriteByte('|')
		b.WriteString(strings.ToLower(h))
	}
	for _, c := range r.ClientCIDRs {
		b.WriteByte('|')
		b.WriteString(c)
	}
	return b.String()
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

func validateTimeouts(cfg *InfrastructureConfig) []error {
	var errs []error
	t := cfg.NetworkTimeoutsSec.Transport
	s := cfg.NetworkTimeoutsSec.Server

	if t.TCP < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.transport.tcp must be >= 0, got %d", t.TCP))
	}
	if t.KeepAlive < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.transport.keep-alive must be >= 0, got %d", t.KeepAlive))
	}
	if t.TLS < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.transport.tls must be >= 0, got %d", t.TLS))
	}
	if t.ResponseHeader < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.transport.response-header must be >= 0, got %d", t.ResponseHeader))
	}
	if t.IdleConn < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.transport.idle-conn must be >= 0, got %d", t.IdleConn))
	}
	if s.Context < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.server.context must be >= 0, got %d", s.Context))
	}
	if s.ReadHeader < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.server.read-header must be >= 0, got %d", s.ReadHeader))
	}
	if s.Idle < 0 {
		errs = append(errs, fmt.Errorf("net-timeouts-s.server.idle must be >= 0, got %d", s.Idle))
	}
	return errs
}

func validateConnectionPool(cfg *InfrastructureConfig) []error {
	var errs []error
	cp := cfg.ConnectionPool

	if cp.MaxIdleConns < 0 {
		errs = append(errs, fmt.Errorf("connection-pool.max-idle-conns must be >= 0, got %d", cp.MaxIdleConns))
	}
	if cp.MaxIdleConnsPerHost < 0 {
		errs = append(errs, fmt.Errorf("connection-pool.max-idle-conns-per-host must be >= 0, got %d", cp.MaxIdleConnsPerHost))
	}
	return errs
}

func validateMirroringConfig(cfg *InfrastructureConfig) []error {
	var errs []error
	mc := cfg.Mirroring

	if mc.Workers < 0 {
		errs = append(errs, fmt.Errorf("mirroring.workers must be >= 0, got %d", mc.Workers))
	}
	if mc.QueueSize < 0 {
		errs = append(errs, fmt.Errorf("mirroring.queue-size must be >= 0, got %d", mc.QueueSize))
	}
	return errs
}

func (cfg *InfrastructureConfig) Validate() []error {
	var errs []error
	errs = append(errs, validateTimeouts(cfg)...)
	errs = append(errs, validateConnectionPool(cfg)...)
	errs = append(errs, validateMirroringConfig(cfg)...)
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
		for _, mirror := range r.Mirrors {
			m := mirror.Upstream
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

// decodeStrict decodes a single YAML document and fails on unknown fields, so
// misspelled keys surface as errors instead of silently loading a zero value.
func decodeStrict(r io.Reader, v any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func ParseConfig(configPath string) (*Config, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading config (%s): %w", configPath, err)
	}
	defer f.Close()

	var config Config
	if err := decodeStrict(f, &config); err != nil {
		return nil, fmt.Errorf("failed yaml unmarshal (%s): %w", configPath, err)
	}
	if err := config.mapNamesToHosts(); err != nil {
		return nil, fmt.Errorf("building name->host map failed (%s): %w", configPath, err)
	}

	return &config, nil
}

func ParseInfrastructureConfig(configPath string) (*InfrastructureConfig, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading infrastructure config (%s): %w", configPath, err)
	}
	defer f.Close()

	var config InfrastructureConfig
	if err := decodeStrict(f, &config); err != nil {
		return nil, fmt.Errorf("failed yaml unmarshal (%s): %w", configPath, err)
	}

	return &config, nil
}
