package experiment

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// ComposeEnv parameterises a docker-compose deployment for one experiment step.
type ComposeEnv struct {
	Backends            int
	HotBias             float64 // ANTAGONIST_BIAS (p_up) for hot-prone backends
	NeutralBias         float64 // ANTAGONIST_BIAS (p_up) for neutral backends
	ColdBias            float64 // ANTAGONIST_BIAS (p_up) for cold-prone backends
	MeanDwellMS         int     // mean dwell time (ms) for the antagonist random walk
	BaseServiceMS       int
	Capacity            int
	CPUsPerBackend      float64
	ProbePoolSize       int
	ProbeRateMultiplier float64
	QRIF                float64
	// ComposeFile is the path to write the generated docker-compose.yml.
	// Defaults to "docker-compose.yml" in the current directory.
	ComposeFile string
}

// Generate writes a docker-compose.yml for env using compose-gen. The
// compose-gen binary must be on PATH or at ./compose-gen.
func Generate(env ComposeEnv) error {
	bin, err := resolveBin("compose-gen", "./compose-gen", "./bin/compose-gen")
	if err != nil {
		return err
	}

	out := env.ComposeFile
	if out == "" {
		out = "docker-compose.yml"
	}

	args := []string{
		fmt.Sprintf("-n=%d", env.Backends),
		fmt.Sprintf("-hot-bias=%.4f", env.HotBias),
		fmt.Sprintf("-neutral-bias=%.4f", env.NeutralBias),
		fmt.Sprintf("-cold-bias=%.4f", env.ColdBias),
		fmt.Sprintf("-mean-dwell-ms=%d", env.MeanDwellMS),
		fmt.Sprintf("-base-service-ms=%d", env.BaseServiceMS),
		fmt.Sprintf("-capacity=%d", env.Capacity),
		fmt.Sprintf("-cpus=%.1f", env.CPUsPerBackend),
		fmt.Sprintf("-probe-pool-size=%d", env.ProbePoolSize),
		fmt.Sprintf("-probe-rate-multiplier=%.4f", env.ProbeRateMultiplier),
		fmt.Sprintf("-qrif=%.4f", env.QRIF),
		fmt.Sprintf("-output=%s", out),
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Build builds all images in the compose file without starting containers.
// Call this once before the experiment loop so Up() can skip rebuilding.
func Build(composeFile string) error {
	args := []string{"compose"}
	if composeFile != "" {
		args = append(args, "-f", composeFile)
	}
	args = append(args, "build")
	return dockerCmd(args...)
}

// Up starts containers from already-built images. Images must have been built
// with Build() beforehand; this skips rebuilding to avoid per-step overhead.
// If service is non-empty, only that service (and its depends_on) is started.
func Up(composeFile, service string) error {
	args := []string{"compose"}
	if composeFile != "" {
		args = append(args, "-f", composeFile)
	}
	args = append(args, "up", "-d")
	if service != "" {
		args = append(args, service)
	}
	return dockerCmd(args...)
}

// Down runs `docker compose down` to stop and remove containers.
func Down(composeFile string) error {
	args := []string{"compose"}
	if composeFile != "" {
		args = append(args, "-f", composeFile)
	}
	args = append(args, "down")
	return dockerCmd(args...)
}

// WaitReady polls url (e.g. "http://localhost:8080/health") until it returns
// HTTP 200 or timeout elapses.
func WaitReady(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("service not ready after %s: %s", timeout, url)
}

func dockerCmd(args ...string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resolveBin(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("binary not found: tried %v", names)
}
