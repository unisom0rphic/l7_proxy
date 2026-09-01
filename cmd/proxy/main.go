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
	"syscall"
	"time"

	"go.yaml.in/yaml/v4"
)

func getenv(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

type Config struct {
	NetworkTimeoutsSec struct {
		Transport struct {
			TCP            int `yaml:"tcp"`
			KeepAlive      int `yaml:"keep_alive"`
			TLS            int `yaml:"tls"`
			ResponseHeader int `yaml:"response_header"`
			IdleConn       int `yaml:"idle_conn"`
		} `yaml:"transport"`

		Server struct {
			Context    int `yaml:"context"`
			ReadHeader int `yaml:"read_header"`
			Idle       int `yaml:"idle"`
		} `yaml:"server"`
	} `yaml:"net_timeouts_s"`

	Upstreams []struct {
		Name    string `yaml:"name"`
		Host    string `yaml:"host"`
		Timeout int    `yaml:"timeout_ms"`
	} `yaml:"upstreams"`

	Routes []struct {
		Rules struct {
			PathPrefix string `yaml:"path_prefix"`
		} `yaml:"rules"`
		Upstream string `yaml:"upstream"`
		Mirror   struct {
			Upstream string  `yaml:"upstream"`
			Ratio    float64 `yaml:"ratio"`
		} `yaml:"mirror"`
	} `yaml:"routes"`
}

func (config *Config) findHostByName(name string) (string, error) {
	for _, upstream := range config.Upstreams {
		if upstream.Name == name {
			return upstream.Host, nil
		}
	}

	return "", errors.New("Host not found")
}

// Don't change signature
func (config *Config) decideRoute(path string) (*url.URL, error) {
	log.Printf("[decideRoute]: Received input: %v\n", path)
	for _, route := range config.Routes {
		routePath := route.Rules.PathPrefix
		if routePath == path {
			name := route.Upstream
			host, err := config.findHostByName(name)

			if err != nil {
				log.Printf("[decidePath]: %v\n", err)
				return nil, errors.New("Path not found")
			}

			log.Printf("HOST: %v, path: %v\n", host, path)

			url, err := url.Parse(host)
			if err != nil {
				log.Printf("[decidePath]: Error parsing host url from config: %v\n", err)
			}
			url.Path = "/api"
			return url, nil
		}
	}

	return nil, errors.New("Path not found")
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
	backendURLs := make([]*url.URL, 0)
	for _, upstream := range config.Upstreams {
		backendURL, err := url.Parse(upstream.Host)
		if err != nil {
			log.Fatalf("Error parsing the url: %v\n", err)
		}
		backendURLs = append(backendURLs, backendURL)
	}

	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }
	timeoutsTransport := config.NetworkTimeoutsSec.Transport
	timeoutsServer := config.NetworkTimeoutsSec.Server

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			route, err := config.decideRoute(r.In.URL.Path)

			if err != nil {
				ctx := context.WithValue(r.Out.Context(), "proxyError", http.StatusBadRequest)
				r.Out = r.Out.WithContext(ctx)
				// FIXME: ErrorHandler is triggered because scheme is ""
				// because the route wasn't found, not after entering this bloc
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
				http.Error(w, "Bad request: invalid route", http.StatusBadRequest)
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
