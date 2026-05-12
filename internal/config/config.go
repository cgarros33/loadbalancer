package config

import (
	"encoding/json"
	"os"
	"time"
)

type Config struct {
	LoadBalancer LoadBalancerConfig `json:"loadbalancer"`
	Prequal      PrequalConfig      `json:"prequal"`
	Servers      []ServerConfig     `json:"servers"`
	Monitoring   MonitoringConfig   `json:"monitoring"`
}

type LoadBalancerConfig struct {
	Port           string `json:"port"`
	ReadTimeoutMs  int    `json:"read_timeout_ms"`
	WriteTimeoutMs int    `json:"write_timeout_ms"`
	Algorithm      string `json:"algorithm"`
}

type PrequalConfig struct {
	SelectionChoices int     `json:"selection_choices"`
	QRIF             float64 `json:"qrif"`
	ProbeIntervalMs  int     `json:"probe_interval_ms"`
	ProbeTimeoutMs   int     `json:"probe_timeout_ms"`
	HealthCheckPath  string  `json:"health_check_path"`
}

type ServerConfig struct {
	ID      string  `json:"id"`
	Address string  `json:"address"`
	Port    string  `json:"port"`
	CPULoad int     `json:"cpu_load"`
	CPUs    float64 `json:"cpus"`
}

type MonitoringConfig struct {
	PrometheusPort  string `json:"prometheus_port"`
	GrafanaPort     string `json:"grafana_port"`
	GrafanaUser     string `json:"grafana_user"`
	GrafanaPassword string `json:"grafana_password"`
	ScrapeInterval  string `json:"scrape_interval"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	cfg := &Config{}
	if err := json.NewDecoder(file).Decode(cfg); err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	// LoadBalancer defaults
	if cfg.LoadBalancer.Port == "" {
		cfg.LoadBalancer.Port = "8080"
	}
	if cfg.LoadBalancer.ReadTimeoutMs == 0 {
		cfg.LoadBalancer.ReadTimeoutMs = 5000
	}
	if cfg.LoadBalancer.WriteTimeoutMs == 0 {
		cfg.LoadBalancer.WriteTimeoutMs = 10000
	}
	if cfg.LoadBalancer.Algorithm == "" {
		cfg.LoadBalancer.Algorithm = "prequal"
	}

	// Prequal defaults
	if cfg.Prequal.SelectionChoices == 0 {
		cfg.Prequal.SelectionChoices = 2
	}
	if cfg.Prequal.QRIF == 0 {
		cfg.Prequal.QRIF = 0.84
	}
	if cfg.Prequal.ProbeIntervalMs == 0 {
		cfg.Prequal.ProbeIntervalMs = 1000
	}
	if cfg.Prequal.ProbeTimeoutMs == 0 {
		cfg.Prequal.ProbeTimeoutMs = 2000
	}
	if cfg.Prequal.HealthCheckPath == "" {
		cfg.Prequal.HealthCheckPath = "/health"
	}
}

// Helper methods for time.Duration conversions
func (c *Config) ProbeInterval() time.Duration {
	return time.Duration(c.Prequal.ProbeIntervalMs) * time.Millisecond
}

func (c *Config) ProbeTimeout() time.Duration {
	return time.Duration(c.Prequal.ProbeTimeoutMs) * time.Millisecond
}

func (c *Config) ReadTimeout() time.Duration {
	return time.Duration(c.LoadBalancer.ReadTimeoutMs) * time.Millisecond
}

func (c *Config) WriteTimeout() time.Duration {
	return time.Duration(c.LoadBalancer.WriteTimeoutMs) * time.Millisecond
}
