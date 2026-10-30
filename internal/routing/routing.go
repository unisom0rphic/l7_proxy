package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
)

type Router struct {
	configPath  string
	atomicIndex atomic.Pointer[routeIndex]
	wg          sync.WaitGroup
}

func (r *Router) Wait() { r.wg.Wait() }

func (router *Router) index() *routeIndex { return router.atomicIndex.Load() }

type Decision struct {
	Upstream string
	Timeout  time.Duration
	Target   *url.URL
	Mirrors  []mirroring.Target
}

type candidate struct {
	route      *config.Route
	pathSuffix string
	regexMatch []int
}

// routeIndex is the compiled form of config.Config: routes grouped by the way
// they match. It is immutable once built, so a reload publishes a new one with a
// single atomic store.
type routeIndex struct {
	cfg    *config.Config
	exact  map[string][]*config.Route
	prefix *trie
	regex  []*config.Route
}

func newRouteIndex(cfg *config.Config) *routeIndex {
	idx := &routeIndex{
		cfg:    cfg,
		exact:  make(map[string][]*config.Route),
		prefix: newTrie(),
	}
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		switch {
		case route.Rule.Path != "":
			idx.exact[route.Rule.Path] = append(idx.exact[route.Rule.Path], route)
		case route.Rule.PathPrefix != "":
			idx.prefix.insert(route.Rule.PathPrefix, route)
		case route.Rule.Regex != "":
			idx.regex = append(idx.regex, route)
		}
	}
	return idx
}

// MatchExact returns the first route registered for exactly path whose
// conditions match r.
func (idx *routeIndex) MatchExact(r *http.Request, path string) (candidate, bool) {
	for _, route := range idx.exact[path] {
		if matchConditions(route.Rule, r) {
			return candidate{route: route}, true
		}
	}
	return candidate{}, false
}

// MatchPrefix returns the route with the longest registered prefix of path whose
// conditions match r. Routes sharing a prefix are tried in config order.
func (idx *routeIndex) MatchPrefix(r *http.Request, path string) (candidate, bool) {
	var (
		best candidate
		ok   bool
	)
	idx.prefix.match(path, func(routes []*config.Route, depth int) bool {
		for _, route := range routes {
			if matchConditions(route.Rule, r) {
				best, ok = candidate{route: route, pathSuffix: path[depth:]}, true
				return true
			}
		}
		return false
	})
	return best, ok
}

// Decides which API route to use for a given http.Request
//
// Priority:
//
//	exact (path == request path)
//	prefix (longest match wins)
//	regex  (first match in cfg.Routes order wins)
func (router *Router) DecideRoute(r *http.Request) (Decision, error) {
	if r.URL == nil {
		return Decision{}, errors.New("empty url")
	}
	path := r.URL.Path
	if path == "" {
		return Decision{}, errors.New("empty url")
	}
	slog.Debug("decideRoute: received input", "path", path)

	idx := router.index()

	if c, ok := matchExact(idx, r, path); ok {
		return finalize(idx, c, r)
	}

	if c, ok := matchPrefix(idx, r, path); ok {
		return finalize(idx, c, r)
	}

	if c, ok := matchRegex(idx, r, path); ok {
		return finalize(idx, c, r)
	}

	slog.Debug("decideRoute: no match", "path", path)
	return Decision{}, errors.New("route not found")
}

func matchExact(idx *routeIndex, r *http.Request, path string) (candidate, bool) {
	return idx.MatchExact(r, path)
}

func matchPrefix(idx *routeIndex, r *http.Request, path string) (candidate, bool) {
	return idx.MatchPrefix(r, path)
}

// matchRegex returns the *first* regex route (in config order) whose compiled
// regex matches path and whose method/headers/hosts/CIDRs all match.
func matchRegex(idx *routeIndex, r *http.Request, path string) (candidate, bool) {
	for _, route := range idx.regex {
		if !matchConditions(route.Rule, r) {
			continue
		}
		re := route.Rule.CompiledRegex()
		if re == nil {
			// ParseConfig guarantee this never happens for a
			// regex rule. A nil here means the config was assembled
			// without either; skip rather than compile per request.
			slog.Error("decideRoute: regex rule has no compiled regex",
				"regex", route.Rule.Regex)
			continue
		}
		m := re.FindStringSubmatchIndex(path)
		if m == nil {
			continue
		}
		slog.Debug("found regex", "regex", route.Rule.Regex, "path", path)
		return candidate{
			route:      route,
			regexMatch: m,
		}, true
	}
	return candidate{}, false
}

// matchConditions checks everything about the request except the path shape:
// method, headers, hosts, and client CIDRs.
func matchConditions(rule config.Rule, r *http.Request) bool {
	if method := rule.Method; method != "" && method != r.Method {
		return false
	}

	for _, h := range rule.Headers {
		if !headerMatches(h, r.Header) {
			return false
		}
	}

	if len(rule.Hosts) > 0 && !hostMatches(rule.Hosts, r.Host) {
		return false
	}

	if len(rule.ClientCIDRs) > 0 && !clientIPMatches(rule.ClientCIDRs, r.RemoteAddr) {
		return false
	}

	return true
}

func headerMatches(h config.HeaderMatch, headers http.Header) bool {
	values := headers.Values(h.Name)
	if len(values) == 0 {
		return false
	}
	switch h.EffectiveOp() {
	case config.HeaderPresent:
		return true
	case config.HeaderEquals:
		for _, v := range values {
			if v == h.Value {
				return true
			}
		}
	case config.HeaderPrefix:
		for _, v := range values {
			if strings.HasPrefix(v, h.Value) {
				return true
			}
		}
	case config.HeaderContains:
		for _, v := range values {
			if strings.Contains(v, h.Value) {
				return true
			}
		}
	}
	return false
}

func hostMatches(hosts []string, host string) bool {
	h := hostOnly(host)
	for _, pattern := range hosts {
		p := strings.ToLower(pattern)
		if strings.HasPrefix(p, "*.") {
			// h must end with ".rest" and be strictly longer than
			// "rest"
			if strings.HasSuffix(h, p[1:]) && len(h) > len(p)-1 {
				return true
			}
			continue
		}
		if h == p {
			return true
		}
	}
	return false
}

// hostOnly strips the port from an r.Host value and lowercases the result.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(h)
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.ToLower(host)
}

func clientIPMatches(cidrs []string, remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, netw, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if netw.Contains(ip) {
			return true
		}
	}
	return false
}

// finalize resolves the winning candidate into a Decision.
func finalize(idx *routeIndex, c candidate, r *http.Request) (Decision, error) {
	cfg := idx.cfg

	host, err := resolveHost(cfg, c.route.Upstream)
	if err != nil {
		return Decision{}, err
	}

	var target *url.URL
	if c.regexMatch != nil {
		expanded := expandRegexRewrite(c.route.Rule.Rewrite, r.URL.Path, c.regexMatch)
		target = buildTarget(host, expanded, "", r.URL.RawQuery)
	} else {
		rewrite := effectiveRewrite(c.route.Rule)
		target = buildTarget(host, rewrite, c.pathSuffix, r.URL.RawQuery)
	}

	timeout := discoverTimeout(cfg, c.route.Upstream)

	mirrors, err := resolveMirrors(cfg, c.route.Mirrors)
	if err != nil {
		return Decision{}, err
	}

	slog.Debug("decideRoute: route found", "host", host, "mirrors", len(mirrors))
	return Decision{Upstream: c.route.Upstream, Timeout: timeout, Target: target, Mirrors: mirrors}, nil
}

// Searches timeout for current upstream (linearly)
func discoverTimeout(cfg *config.Config, up string) time.Duration {
	for _, u := range cfg.Upstreams {
		if u.Name == up {
			return time.Duration(u.Timeout)
		}
	}
	return 0
}

func resolveHost(cfg *config.Config, name string) (*url.URL, error) {
	host, ok := cfg.NameToHost[name]
	if !ok {
		// If we reach this branch it means the config was incorrectly parsed,
		// which means serious issue inside the parser that bypasses the tests.
		// TODO: consider panicking when config validator ensures each name is mapped
		// to the host.
		slog.Error("decideRoute: host not found in upstreams", "upstream", name)
		return nil, errors.New("URL host not mapped during config parse")
	}
	return host, nil
}

func resolveMirrors(cfg *config.Config, mirrors []config.Mirror) ([]mirroring.Target, error) {
	if len(mirrors) == 0 {
		return nil, nil
	}
	out := make([]mirroring.Target, 0, len(mirrors))
	for _, m := range mirrors {
		host, ok := cfg.NameToHost[m.Upstream]
		if !ok {
			slog.Error("decideRoute: mirror host not found in upstreams", "upstream", m.Upstream)
			return nil, fmt.Errorf("mirror %q not mapped during config parse", m.Upstream)
		}
		out = append(out, mirroring.Target{URL: host, Ratio: m.Ratio})
	}
	return out, nil
}

// effectiveRewrite returns the outbound path base: Rule.Rewrite if set,
// otherwise the matched Path or PathPrefix.
func effectiveRewrite(rule config.Rule) string {
	if rule.Rewrite != "" {
		return rule.Rewrite
	}
	if rule.Path != "" {
		return rule.Path
	}
	return rule.PathPrefix
}

// expandRegexRewrite substitutes capture references in a rewrite template using
// the submatch indices from FindStringSubmatchIndex:
//
//	$0        whole match
//	$1..$9    capture groups (nginx-style single digit; $10 means $1
//	          followed by "0", not group 10)
//	$$        literal '$'
//
// Any other '$' is left as-is, so a template like "/v2/$data" round-trips
// unchanged. An unmatched optional group expands to nothing.
// No path suffix for regex routes.
func expandRegexRewrite(template, path string, match []int) string {
	if match == nil {
		return template
	}
	var b strings.Builder
	b.Grow(len(template))
	for i := 0; i < len(template); i++ {
		if template[i] != '$' || i+1 >= len(template) {
			b.WriteByte(template[i])
			continue
		}
		switch next := template[i+1]; {
		case next == '$':
			b.WriteByte('$')
			i++
		case next >= '0' && next <= '9':
			n := int(next - '0')
			// match[2*n], match[2*n+1] are start/end of group n; both
			// are -1 for an unmatched optional group.
			if 2*n+1 < len(match) && match[2*n] >= 0 {
				b.WriteString(path[match[2*n]:match[2*n+1]])
			}
			i++
		default:
			b.WriteByte(template[i])
		}
	}
	return b.String()
}

// buildTarget constructs the outbound URL for the winning candidate.
// pathSuffix is "" for exact matches; for prefixes it's the remainder after
// the matched prefix.
func buildTarget(host *url.URL, rewrite, pathSuffix, rawQuery string) *url.URL {
	return &url.URL{
		Scheme:   host.Scheme,
		Host:     host.Host,
		Path:     rewrite + pathSuffix,
		RawQuery: rawQuery,
	}
}

func CreateFromConfig(ctx context.Context, configPath string) (*Router, error) {
	cfg, err := config.ParseConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	errs := cfg.Validate()
	if len(errs) > 0 {
		for _, e := range errs {
			slog.Error("config validation error", "err", e)
		}
		return nil, errInvalidConfig
	}

	router, err := NewRouter(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create router from config: %w", err)
	}
	router.configPath = configPath

	if err := router.startMonitoringConfigUpdates(ctx); err != nil {
		return nil, err
	}

	return router, nil
}

func NewRouter(cfg *config.Config) (*Router, error) {
	router := &Router{}
	router.swapConfig(cfg)
	return router, nil
}

// Starts monitoring config file for updates, automatically swaps if notices changes.
// A removed config is reported and ignored: the router keeps serving the last
// valid config until the file reappears.
func (r *Router) startMonitoringConfigUpdates(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create a new watcher: %w", err)
	}

	absTarget, err := filepath.Abs(r.configPath)
	if err != nil {
		_ = watcher.Close()
		return fmt.Errorf("failed to obtain absolute path: %w", err)
	}

	parentDir := filepath.Dir(absTarget)

	if err := watcher.Add(parentDir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("adding directory (%s) to watch list failed: %w", parentDir, err)
	}
	slog.Info("monitoring changes", "config", r.configPath)

	// Listener loop
	r.wg.Go(func() {
		defer watcher.Close()

		configCooldown := time.NewTimer(0)
		if !configCooldown.Stop() {
			<-configCooldown.C
		}
		defer configCooldown.Stop()
		onCoolDown := false
		pendingReload := ""

		reload := func(source string) {
			if onCoolDown {
				// Cannot reload yet, but the change must not be lost: the
				// cooldown ends with a reload of the newest state.
				pendingReload = source
				return
			}
			r.reloadConfig(source)
			onCoolDown = true
			configCooldown.Reset(reloadCooldown)
		}

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		slog.Info("subscribed to SIGHUP")
		defer signal.Stop(sigCh)

		for {
			select {
			case <-ctx.Done():
				slog.Info("monitor shutting down due to context cancellation")
				return

			case <-sigCh:
				// SIGHUP will be intercepted if using `go run .`,
				// this execution path is only accessible from the compiled binary
				reload("SIGHUP")

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				absEventPath, err := filepath.Abs(event.Name)
				if err != nil {
					continue
				}

				if !isTargetPath(absEventPath, absTarget) {
					continue
				}

				switch {
				case event.Has(fsnotify.Remove), event.Has(fsnotify.Rename):
					if _, err := os.Stat(absTarget); err != nil {
						slog.Error("Config deleted", "path", r.configPath, "event", event.Name)
						metrics.ConfigReloadErrorsTotal.WithLabelValues("remove").Inc()
						continue
					}
					reload("config replaced")
				case event.Has(fsnotify.Write), event.Has(fsnotify.Create):
					reload("config modified")
				}
			case <-configCooldown.C:
				onCoolDown = false
				if pendingReload != "" {
					source := pendingReload
					pendingReload = ""
					reload(source)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("watcher error", "error", err)
			}

		}
	})

	return nil
}

// isTargetPath reports whether an fsnotify event refers to the watched config
// file. Paths can be spelled differently from configPath (Windows casing short
// names, symlinks), so identity is compared where possible.
func isTargetPath(eventPath, target string) bool {
	if strings.EqualFold(eventPath, target) {
		return true
	}
	eventInfo, err := os.Stat(eventPath)
	if err != nil {
		return false
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return false
	}
	return os.SameFile(eventInfo, targetInfo)
}

const reloadCooldown = 1 * time.Second

var errInvalidConfig = errors.New("invalid config")

// reloadConfig re-reads the config file and swaps it in. A failed reload is
// logged and counted, and leaves the previous config in place.
func (router *Router) reloadConfig(source string) {
	slog.Info("reloading config", "source", source, "file", router.configPath)
	if err := router.updateConfig(); err != nil {
		reason := "parse"
		if errors.Is(err, errInvalidConfig) {
			reason = "validate"
		}
		slog.Error("error updating config", "source", source, "reason", reason, "error", err)
		metrics.ConfigReloadErrorsTotal.WithLabelValues(reason).Inc()
	}
}

// Used to apply changes in router`s config file
func (router *Router) updateConfig() error {
	cfg, err := config.ParseConfig(router.configPath)
	if err != nil {
		return fmt.Errorf("error parsing config: %w", err)
	}

	errs := cfg.Validate()
	if len(errs) > 0 {
		for _, e := range errs {
			slog.Error("config validation error", "err", e)
		}
		return errInvalidConfig
	}

	router.swapConfig(cfg)
	return nil
}

func (router *Router) swapConfig(cfg *config.Config) {
	router.atomicIndex.Store(newRouteIndex(cfg))
}

func (router *Router) Config() *config.Config {
	return router.index().cfg
}
