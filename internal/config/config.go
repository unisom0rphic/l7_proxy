package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v4"
)

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
	Rewrite    string `yaml:"rewrite"`
	Method     string `yaml:"method"`
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

type Config struct {
	NetworkTimeoutsSec struct {
		Transport TransportTimeoutes `yaml:"transport"`
		Server    ServerTimeoutes    `yaml:"server"`
	} `yaml:"net_timeouts_s"`
	Upstreams []Upstream `yaml:"upstreams"`
	Routes    []Route    `yaml:"routes"`
}

func ParseConfig(configPath string) (*Config, error) {
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("Error reading config (%s): %v\n", configPath, err)
	}

	var config Config
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		return nil, fmt.Errorf("Failed yaml umarshal (%s): %v\n", configPath, err)
	}

	return &config, nil
}
