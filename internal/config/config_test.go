package config

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Write a minimal test config
	content := `{
		"loadbalancer": {
			"port": "9090",
			"algorithm": "prequal"
		},
		"prequal": {
			"selection_choices": 3,
			"qrif": 0.75,
			"probe_interval_ms": 500,
			"probe_timeout_ms": 1500,
			"health_check_path": "/healthz"
		},
		"servers": [
			{"id": "s1", "address": "s1:80", "port": "80", "cpu_load": 50, "cpus": 2.0},
			{"id": "s2", "address": "s2:80", "port": "80", "cpu_load": 0, "cpus": 1.0}
		],
		"monitoring": {
			"prometheus_port": "9090",
			"grafana_port": "3001"
		}
	}`

	tmpFile, err := os.CreateTemp("", "test-config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify loadbalancer section
	if cfg.LoadBalancer.Port != "9090" {
		t.Errorf("Expected port '9090', got '%s'", cfg.LoadBalancer.Port)
	}
	if cfg.LoadBalancer.Algorithm != "prequal" {
		t.Errorf("Expected algorithm 'prequal', got '%s'", cfg.LoadBalancer.Algorithm)
	}

	// Verify prequal section
	if cfg.Prequal.SelectionChoices != 3 {
		t.Errorf("Expected SelectionChoices 3, got %d", cfg.Prequal.SelectionChoices)
	}
	if cfg.Prequal.QRIF != 0.75 {
		t.Errorf("Expected QRIF 0.75, got %f", cfg.Prequal.QRIF)
	}
	if cfg.Prequal.ProbeIntervalMs != 500 {
		t.Errorf("Expected ProbeIntervalMs 500, got %d", cfg.Prequal.ProbeIntervalMs)
	}
	if cfg.Prequal.HealthCheckPath != "/healthz" {
		t.Errorf("Expected HealthCheckPath '/healthz', got '%s'", cfg.Prequal.HealthCheckPath)
	}

	// Verify servers
	if len(cfg.Servers) != 2 {
		t.Fatalf("Expected 2 servers, got %d", len(cfg.Servers))
	}
	if cfg.Servers[0].ID != "s1" {
		t.Errorf("Expected server[0].ID 's1', got '%s'", cfg.Servers[0].ID)
	}
	if cfg.Servers[0].CPULoad != 50 {
		t.Errorf("Expected server[0].CPULoad 50, got %d", cfg.Servers[0].CPULoad)
	}
	if cfg.Servers[0].CPUs != 2.0 {
		t.Errorf("Expected server[0].CPUs 2.0, got %f", cfg.Servers[0].CPUs)
	}
	if cfg.Servers[1].CPULoad != 0 {
		t.Errorf("Expected server[1].CPULoad 0, got %d", cfg.Servers[1].CPULoad)
	}

	// Verify duration helpers
	if cfg.ProbeInterval().Milliseconds() != 500 {
		t.Errorf("Expected ProbeInterval 500ms, got %dms", cfg.ProbeInterval().Milliseconds())
	}
	if cfg.ProbeTimeout().Milliseconds() != 1500 {
		t.Errorf("Expected ProbeTimeout 1500ms, got %dms", cfg.ProbeTimeout().Milliseconds())
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	// Minimal config — everything should get defaults
	content := `{
		"servers": [
			{"id": "s1", "address": "s1:80", "port": "80", "cpu_load": 0, "cpus": 1.0}
		]
	}`

	tmpFile, err := os.CreateTemp("", "test-config-defaults-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.LoadBalancer.Port != "8080" {
		t.Errorf("Expected default port '8080', got '%s'", cfg.LoadBalancer.Port)
	}
	if cfg.LoadBalancer.Algorithm != "prequal" {
		t.Errorf("Expected default algorithm 'prequal', got '%s'", cfg.LoadBalancer.Algorithm)
	}
	if cfg.Prequal.SelectionChoices != 2 {
		t.Errorf("Expected default SelectionChoices 2, got %d", cfg.Prequal.SelectionChoices)
	}
	if cfg.Prequal.QRIF != 0.84 {
		t.Errorf("Expected default QRIF 0.84, got %f", cfg.Prequal.QRIF)
	}
	if cfg.Prequal.ProbeIntervalMs != 1000 {
		t.Errorf("Expected default ProbeIntervalMs 1000, got %d", cfg.Prequal.ProbeIntervalMs)
	}
	if cfg.Prequal.ProbeTimeoutMs != 2000 {
		t.Errorf("Expected default ProbeTimeoutMs 2000, got %d", cfg.Prequal.ProbeTimeoutMs)
	}
	if cfg.Prequal.HealthCheckPath != "/health" {
		t.Errorf("Expected default HealthCheckPath '/health', got '%s'", cfg.Prequal.HealthCheckPath)
	}
	if cfg.LoadBalancer.ReadTimeoutMs != 5000 {
		t.Errorf("Expected default ReadTimeoutMs 5000, got %d", cfg.LoadBalancer.ReadTimeoutMs)
	}
	if cfg.LoadBalancer.WriteTimeoutMs != 10000 {
		t.Errorf("Expected default WriteTimeoutMs 10000, got %d", cfg.LoadBalancer.WriteTimeoutMs)
	}
}

func TestLoadConfigInvalidPath(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.json")
	if err == nil {
		t.Error("Expected error for nonexistent config file")
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-config-bad-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString("{invalid json}"); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	_, err = LoadConfig(tmpFile.Name())
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}
