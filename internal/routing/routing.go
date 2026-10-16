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
	configPath   string
	atomicConfig atomic.Pointer[config.Config]
	wg           sync.WaitGroup
}

func (r *Router) Wait() { r.wg.Wait() }

type Decision struct {
	Target  *url.URL
	Mirrors []mirroring.MirrorTarget
}

type candidate struct {
	route       *config.Route
	pathSuffix  string
	specificity int
}

// Decides which API route to use for a given http.Request
func (router *Router) DecideRoute(r *http.Request) (Decision, error) {
	if r.URL == nil {
		return Decision{}, errors.New("empty url")
	}
	path := r.URL.Path
	if path == "" {
		return Decision{}, errors.New("empty url")
	}
	slog.Debug("decideRoute: received input", "path", path)

	cfg := router.Config()

	if c, ok := matchExact(cfg, r, path); ok {
		return finalize(cfg, c, r)
	}

	cs := matchPrefix(cfg, r, path)
	c, ok := pickBest(cs)
	if !ok {
		slog.Debug("decideRoute: no match", "path", path)
		return Decision{}, errors.New("route not found")
	}

	return finalize(cfg, c, r)
}

// matchExact returns the first route whose Rule.Path equals path and whose
// method/headers/hosts/CIDRs all match.
func matchExact(cfg *config.Config, r *http.Request, path string) (candidate, bool) {
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		if route.Rule.Path == "" || route.Rule.Path != path {
			continue
		}
		if !matchConditions(route.Rule, r) {
			continue
		}
		rewrite := effectiveRewrite(route.Rule)
		return candidate{
			route:       route,
			pathSuffix:  "",
			specificity: len(rewrite),
		}, true
	}
	return candidate{}, false
}

// matchPrefix returns every prefix route whose path prefix matches and whose
// conditions all match. Candidates are unresolved.
func matchPrefix(cfg *config.Config, r *http.Request, path string) []candidate {
	var cs []candidate
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		prefix := route.Rule.PathPrefix
		if prefix == "" {
			continue
		}
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if !matchConditions(route.Rule, r) {
			continue
		}
		slog.Debug("found prefix", "prefix", prefix, "path", path)
		rewrite := effectiveRewrite(route.Rule)
		suffix := path[len(prefix):]
		cs = append(cs, candidate{
			route:       route,
			pathSuffix:  suffix,
			specificity: len(rewrite + suffix),
		})
	}
	return cs
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

// pickBest selects the winning candidate.
func pickBest(cs []candidate) (candidate, bool) {
	// Looking for the longest URL
	var best candidate
	found := false
	for _, c := range cs {
		if !found || c.specificity > best.specificity {
			best = c
			found = true
		}
	}
	return best, found
}

// finalize resolves the winning candidate into a Decision.
func finalize(cfg *config.Config, c candidate, r *http.Request) (Decision, error) {
	host, err := resolveHost(cfg, c.route.Upstream)
	if err != nil {
		return Decision{}, err
	}

	rewrite := effectiveRewrite(c.route.Rule)
	target := buildTarget(host, rewrite, c.pathSuffix, r.URL.RawQuery)

	mirrors, err := resolveMirrors(cfg, c.route.Mirrors)
	if err != nil {
		return Decision{}, err
	}

	slog.Debug("decideRoute: route found", "host", host, "mirrors", len(mirrors))
	return Decision{Target: target, Mirrors: mirrors}, nil
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

func resolveMirrors(cfg *config.Config, mirrors []config.Mirror) ([]mirroring.MirrorTarget, error) {
	if len(mirrors) == 0 {
		return nil, nil
	}
	out := make([]mirroring.MirrorTarget, 0, len(mirrors))
	for _, m := range mirrors {
		host, ok := cfg.NameToHost[m.Upstream]
		if !ok {
			slog.Error("decideRoute: mirror host not found in upstreams", "upstream", m.Upstream)
			return nil, fmt.Errorf("mirror %q not mapped during config parse", m.Upstream)
		}
		out = append(out, mirroring.MirrorTarget{URL: host, Ratio: m.Ratio})
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
		return nil, fmt.Errorf("invalid config")
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
func (router *Router) startMonitoringConfigUpdates(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create a new watcher: %w", err)
	}

	absTarget, err := filepath.Abs(router.configPath)
	if err != nil {
		watcher.Close()
		return fmt.Errorf("failed to obtain absolute path: %w", err)
	}

	parentDir := filepath.Dir(absTarget)

	if err := watcher.Add(parentDir); err != nil {
		watcher.Close()
		return fmt.Errorf("adding directory (%s) to watch list failed: %w", parentDir, err)
	}
	slog.Info("monitoring changes", "config", router.configPath)

	// Listener loop
	router.wg.Go(func() {
		defer watcher.Close()

		configCooldown := time.NewTimer(0)
		if !configCooldown.Stop() {
			<-configCooldown.C
		}
		defer configCooldown.Stop()
		onCoolDown := false

		reload := func(source string) {
			if onCoolDown {
				return
			}
			router.reloadConfig(source)
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
						slog.Error("Config deleted", "path", router.configPath, "event", event.Name)
						metrics.ConfigReloadErrorsTotal.WithLabelValues("remove").Inc()
						continue
					}
					reload("config replaced")
				case event.Has(fsnotify.Write), event.Has(fsnotify.Create):
					reload("config modified")
				}
			case <-configCooldown.C:
				onCoolDown = false
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
	router.atomicConfig.Store(cfg)
}

func (router *Router) Config() *config.Config {
	return router.atomicConfig.Load()
}
