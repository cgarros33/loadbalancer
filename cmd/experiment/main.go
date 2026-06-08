package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/omarshaarawi/loadbalancer/internal/experiment"
	"github.com/omarshaarawi/loadbalancer/internal/loadgen"
)

func main() {
	if execPath, err := os.Executable(); err == nil {
		binDir := filepath.Dir(execPath)
		os.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	expName := flag.String("experiment", "b", "experiment to run: a or b")
	duration := flag.Duration("duration", 30*time.Second, "measurement window per step")
	warmup := flag.Duration("warmup", 10*time.Second, "warm-up duration before each measurement")
	qps := flag.Float64("qps", 100, "target request rate (queries per second)")
	backends := flag.Int("backends", 10, "number of backend containers")
	cpuLoad := flag.Int("cpu-load", 50, "baseline (cold) CPU_LOAD antagonist fraction (0-100)")
	hotFraction := flag.Float64("hot-fraction", 0.0, "fraction of backends that are hot (0.0-1.0)")
	hotCPULoad := flag.Int("hot-cpu-load", 80, "CPU_LOAD for hot backends")
	capacity := flag.Int("capacity", 20, "CAPACITY per backend")
	baseMS := flag.Int("base-service-ms", 5, "BASE_SERVICE_MS per backend")
	qrif := flag.Float64("qrif", 0.84, "QRIF quantile for HCL selection")
	output := flag.String("output", "results.csv", "CSV output path")
	cloudlab := flag.Bool("cloudlab", false, "skip compose lifecycle (backends already running)")
	lbPrequal := flag.String("lb-prequal", "http://localhost:8080", "Prequal LB base URL")
	lbRR := flag.String("lb-rr", "http://localhost:8081", "Round-robin LB base URL")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	var exp experiment.Experiment
	switch *expName {
	case "a", "A":
		exp = experiment.ExperimentA(*qps)
	case "b", "B":
		exp = experiment.ExperimentB()
	default:
		fmt.Fprintf(os.Stderr, "unknown experiment %q; use 'a' or 'b'\n", *expName)
		os.Exit(1)
	}

	csv, err := experiment.NewCSVWriter(*output)
	if err != nil {
		logger.Error("failed to open output", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("starting experiment",
		slog.String("experiment", exp.Name),
		slog.Int("steps", len(exp.Steps)),
		slog.Float64("qps", *qps),
		slog.Duration("duration", *duration),
		slog.Duration("warmup", *warmup),
	)

	composeFile := "docker-compose.yml"

	algoConfigs := []struct {
		algo       string
		url        string
		metricsURL string
	}{
		{"prequal", *lbPrequal, *lbPrequal + "/metrics"},
		{"roundrobin", *lbRR, *lbRR + "/metrics"},
	}

	makeEnv := func(step experiment.Step) experiment.ComposeEnv {
		return experiment.ComposeEnv{
			Backends:            *backends,
			CPULoad:             *cpuLoad,
			HotFraction:         *hotFraction,
			HotCPULoad:          *hotCPULoad,
			BaseServiceMS:       *baseMS,
			Capacity:            *capacity,
			CPUsPerBackend:      1.0,
			ProbePoolSize:       step.ProbePoolSize,
			ProbeRateMultiplier: step.ProbeRateMultiplier,
			QRIF:                *qrif,
			ComposeFile:         composeFile,
		}
	}

	// Build images once before the experiment loop.
	if !*cloudlab {
		if err := experiment.Generate(makeEnv(exp.Steps[0])); err != nil {
			logger.Error("compose-gen failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		logger.Info("building images (once)")
		if err := experiment.Build(composeFile); err != nil {
			logger.Error("docker compose build failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	// Outer loop: one algorithm at a time.
	// Backends start ONCE per algorithm and stay running across all probe-rate
	// steps so their latency trackers accumulate history. Between algorithms we
	// do a full compose down/up to give each algorithm an uncontaminated
	// backend state (e.g. RR's hot-backend queue buildup doesn't carry over).
	for _, ac := range algoConfigs {
		logger.Info("starting algorithm run", slog.String("algo", ac.algo))

		for i, step := range exp.Steps {
			logger.Info("running step",
				slog.String("algo", ac.algo),
				slog.String("label", step.Label),
				slog.Float64("rate_multiplier", step.ProbeRateMultiplier))

			if !*cloudlab {
				env := makeEnv(step)
				if err := experiment.Generate(env); err != nil {
					logger.Error("compose-gen failed", slog.String("error", err.Error()))
					os.Exit(1)
				}
				if err := experiment.Up(composeFile); err != nil {
					logger.Error("docker compose up failed", slog.String("error", err.Error()))
					os.Exit(1)
				}
				if err := experiment.WaitReady(ac.metricsURL, 60*time.Second); err != nil {
					logger.Error("LB not ready", slog.String("addr", ac.metricsURL), slog.String("error", err.Error()))
					os.Exit(1)
				}
				if i == 0 {
					// First step: backends are fresh, give extra settle time.
					time.Sleep(10 * time.Second)
				} else {
					// Subsequent steps: backends are warm, only LBs restarted.
					time.Sleep(3 * time.Second)
				}
			}

			ctx := context.Background()

			go loadgen.Run(ctx, loadgen.Config{
				URL:      ac.url,
				QPS:      *qps,
				Duration: *warmup,
			})
			time.Sleep(*warmup)

			res := loadgen.Run(ctx, loadgen.Config{
				URL:      ac.url,
				QPS:      *qps,
				Duration: *duration,
			})
			rif, _ := loadgen.ScrapeRIF(ac.metricsURL, ac.algo)

			row := experiment.Row{
				Experiment:          exp.Name,
				Algorithm:           ac.algo,
				StepLabel:           step.Label,
				ProbePoolSize:       step.ProbePoolSize,
				ProbeRateMultiplier: step.ProbeRateMultiplier,
				P99Ms:               float64(res.P99.Microseconds()) / 1000.0,
				P999Ms:              float64(res.P999.Microseconds()) / 1000.0,
				RIF_P50:             rif.P50,
				RIF_P75:             rif.P75,
				RIF_P99:             rif.P99,
				Total:               res.Total,
				Errors:              res.Errors,
			}
			if err := csv.Write(row); err != nil {
				logger.Error("csv write failed", slog.String("error", err.Error()))
			}
			logger.Info("step result",
				slog.String("algo", ac.algo),
				slog.String("step", step.Label),
				slog.Float64("p99_ms", row.P99Ms),
				slog.Int64("total", res.Total),
				slog.Int64("errors", res.Errors),
			)
		}

		if !*cloudlab {
			if err := experiment.Down(composeFile); err != nil {
				logger.Error("docker compose down failed", slog.String("error", err.Error()))
			}
			time.Sleep(3 * time.Second)
		}
	}

	logger.Info("experiment complete", slog.String("output", *output))
}
