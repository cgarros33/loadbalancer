//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config mirrors the loadbalancer.json schema (standalone to avoid import issues with //go:build ignore)
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

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func main() {
	cfgPath := "loadbalancer.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Apply defaults
	if cfg.LoadBalancer.Port == "" {
		cfg.LoadBalancer.Port = "8080"
	}
	if cfg.Monitoring.PrometheusPort == "" {
		cfg.Monitoring.PrometheusPort = "9090"
	}
	if cfg.Monitoring.GrafanaPort == "" {
		cfg.Monitoring.GrafanaPort = "3001"
	}
	if cfg.Monitoring.GrafanaUser == "" {
		cfg.Monitoring.GrafanaUser = "admin"
	}
	if cfg.Monitoring.GrafanaPassword == "" {
		cfg.Monitoring.GrafanaPassword = "admin"
	}

	var b strings.Builder

	// --- Header ---
	b.WriteString("# Auto-generated from loadbalancer.json — do not edit manually.\n")
	b.WriteString("# Regenerate with: go run generate-compose.go\n\n")

	b.WriteString("services:\n")

	// Server names for depends_on
	serverNames := make([]string, len(cfg.Servers))
	for i, srv := range cfg.Servers {
		serverNames[i] = srv.ID
	}

	// Format depends_on block
	writeDependsOn := func(b *strings.Builder, names []string) {
		b.WriteString("    depends_on:\n")
		for _, name := range names {
			b.WriteString(fmt.Sprintf("      - %s\n", name))
		}
	}

	// --- LB Prequal ---
	b.WriteString(fmt.Sprintf(`  loadbalancer-prequal:
    build: .
    container_name: lb-prequal
    ports:
      - "%s:%s"
    networks:
      - loadbalancer-net
    volumes:
      - ./loadbalancer.json:/app/loadbalancer.json:ro
    environment:
      - LB_ALGORITHM=prequal
`, cfg.LoadBalancer.Port, cfg.LoadBalancer.Port))
	writeDependsOn(&b, serverNames)
	b.WriteString("\n")

	// --- LB Round-Robin (comparison instance on port+1) ---
	b.WriteString(fmt.Sprintf(`  loadbalancer-rr:
    build: .
    container_name: lb-roundrobin
    ports:
      - "8081:%s"
    networks:
      - loadbalancer-net
    volumes:
      - ./loadbalancer.json:/app/loadbalancer.json:ro
    environment:
      - LB_ALGORITHM=roundrobin
`, cfg.LoadBalancer.Port))
	writeDependsOn(&b, serverNames)
	b.WriteString("\n")

	// --- Backend servers ---
	for _, srv := range cfg.Servers {
		b.WriteString(fmt.Sprintf(`  %s:
    build: ./backend
    container_name: %s
    networks:
      - loadbalancer-net
    environment:
      - SERVER_ID=%s
      - PORT=%s
      - CPU_LOAD=%d
    cpus: %.1f

`, srv.ID, srv.ID, srv.ID, srv.Port, srv.CPULoad, srv.CPUs))
	}

	// --- Prometheus ---
	b.WriteString(fmt.Sprintf(`  prometheus:
    image: prom/prometheus
    ports:
      - "%s:9090"
    volumes:
      - ./config/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
    networks:
      - loadbalancer-net

`, cfg.Monitoring.PrometheusPort))

	// --- Grafana ---
	b.WriteString(fmt.Sprintf(`  grafana:
    image: grafana/grafana
    ports:
      - "%s:3000"
    environment:
      - GF_SECURITY_ADMIN_USER=%s
      - GF_SECURITY_ADMIN_PASSWORD=%s
    volumes:
      - ./config/grafana/provisioning:/etc/grafana/provisioning
      - ./config/grafana/dashboards:/var/lib/grafana/dashboards
    networks:
      - loadbalancer-net
    depends_on:
      - prometheus

`, cfg.Monitoring.GrafanaPort, cfg.Monitoring.GrafanaUser, cfg.Monitoring.GrafanaPassword))

	// --- Network ---
	b.WriteString(`networks:
  loadbalancer-net:
    driver: bridge
`)

	// Write to docker-compose.yml
	if err := os.WriteFile("docker-compose.yml", []byte(b.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing docker-compose.yml: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated docker-compose.yml with %d backend server(s)\n", len(cfg.Servers))
}
