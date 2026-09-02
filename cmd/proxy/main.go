package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v4"
)

// used in 2 places already
func getenv(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

type TransportTimeoutes struct {
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
	// etc
}

type Route struct {
	Rule     Rule   `yaml:"rules"`
	Upstream string `yaml:"upstream"`
	Mirror   struct {
		Upstream string  `yaml:"upstream"`
		Ratio    float64 `yaml:"ratio"`
	} `yaml:"mirror"`
}

// Need a function to parse yaml
// will be useful for runtime atomic config swap
type Config struct {
	NetworkTimeoutsSec struct {
		Transport TransportTimeoutes `yaml:"transport"`
		Server    ServerTimeoutes    `yaml:"server"`
	} `yaml:"net_timeouts_s"`
	Upstreams []Upstream `yaml:"upstreams"`
	Routes    []Route    `yaml:"routes"`
}

// Decides which API route to use given a path
//
// # Don't change signature
// also prob need Router struct
func (config *Config) decideRoute(path string) (*url.URL, error) {
	// TODO: obviously add support for other rules
	log.Printf("[decideRoute]: Received input: %v\n", path)

	// Mapping services` names to hosts` URLs
	urls := make(map[string]*url.URL)
	for _, upstream := range config.Upstreams {
		name := upstream.Name
		host := upstream.Host

		url, err := url.Parse(host)
		if err != nil {
			log.Printf("[decidePath]: Error parsing host url from config: %v\n", err)
		}
		urls[name] = url
	}

	// Path
	for _, route := range config.Routes {
		routePath := route.Rule.Path
		if routePath == path {
			name := route.Upstream
			url, ok := urls[name]

			if !ok {
				log.Println("[decidePath]: Route not found")
				return nil, errors.New("Route not found")
			}

			url.Path = "/api"
			return url, nil
		}
	}

	// Prefix
	for _, route := range config.Routes {
		prefix := route.Rule.PathPrefix
		if prefix == "" {
			continue
		}
		// FIXME: should be different logic, will catch /usersfoo for /users
		if strings.HasPrefix(path, prefix) {
			log.Printf("Found prefix: %v for %v\n", prefix, path)
			upstreamService := route.Upstream
			host, ok := urls[upstreamService]

			if !ok {
				log.Printf(
					"[decideRoute]: host not found in upstreams\nHost %v\nUpstream %v\n",
					host, upstreamService)
				return nil, errors.New("Unknown host URL")
			}

			url := &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				// Path:   path, // strip prefix or some
				Path: "/api",
			}

			return url, nil
		}
	}

	// Headers
	// no idea like we should pass r.In and look at headers?
	// the same with method and query parameters

	log.Printf("[decideRoute]: No match for %v\n", path)
	return nil, errors.New("Route not found")
} // test this function

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	configPath := "config.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Error reading config (%s): %v\n", configPath, err)
	}

	var config Config
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		log.Fatalf("Failed yaml umarshal (%s): %v\n", configPath, err)
	}

	log.Printf("CONFIG: %v\n", config)

	// TODO: route policy
	// TODO: hot reload
	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }
	timeoutsTransport := config.NetworkTimeoutsSec.Transport
	timeoutsServer := config.NetworkTimeoutsSec.Server

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			route, err := config.decideRoute(r.In.URL.Path)
			log.Println("DECIDED: ", route)

			if err != nil {
				ctx := context.WithValue(r.Out.Context(), "proxyError", http.StatusNotFound)
				r.Out = r.Out.WithContext(ctx)
				// FIXME: ErrorHandler is triggered because scheme is ""
				// because the route wasn't found, not because we entered this block
				// it works but it DOESN'T LET ME SLEEP
				return
			}

			log.Printf("Route: %v\n", route)

			r.SetXForwarded()
			r.Out.URL = route
			r.Out.Host = route.Host
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("HTTP error on %s %s: %v\n", r.Method, r.URL.Path, err)
			e := r.Context().Value("proxyError")

			if e == http.StatusBadRequest {
				http.Error(w, "Bad request: ", http.StatusBadRequest)
				return
			} else if e == http.StatusNotFound {
				http.NotFound(w, r)
				return
			} else if errors.Is(err, context.DeadlineExceeded) {
				http.Error(w, "Request timed out on the server", http.StatusGatewayTimeout)
				return
			} else {
				http.Error(w, "Bad Gateway: unexpected error", http.StatusBadGateway)
				return
			}
		},

		ModifyResponse: func(resp *http.Response) error {
			method := resp.Request.Method
			status := resp.StatusCode
			path := resp.Request.URL.Path
			log.Printf("Method: %v | StatusCode: %v | Path: %v\n", method, status, path)
			return nil
		},

		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   toSec(timeoutsTransport.TCP),
				KeepAlive: toSec(timeoutsTransport.KeepAlive),
			}).DialContext,
			TLSHandshakeTimeout:   toSec(timeoutsTransport.TLS),
			ResponseHeaderTimeout: toSec(timeoutsTransport.ResponseHeader),
			IdleConnTimeout:       toSec(timeoutsTransport.IdleConn),
		},
	}

	port := getenv("PORT", "8080")

	server := &http.Server{
		Addr: ":" + port,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// FIXME: context timeout depends on server timeout
			// (user can define how long to wait for the response body)
			// SOLUTION: basically just map name to timeout on config init
			ctx, cancel := context.WithTimeout(r.Context(), toSec(timeoutsServer.Context))
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
		}),
		ReadHeaderTimeout: toSec(timeoutsServer.ReadHeader),
		IdleTimeout:       toSec(timeoutsServer.Idle),
	}

	go func() {
		log.Println("Proxy running at ", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server error: %v\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Shutdown failed: %v\n", err)
	}

	// clean up will be here
	log.Println("Server stopped")

}
