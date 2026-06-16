package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	drain := flag.Duration("drain", 5*time.Second, "cool-down between warmup end and measurement start")
	qps := flag.Float64("qps", 100, "target request rate (queries per second)")
	backends := flag.Int("backends", 10, "number of backend containers")
	// See cmd/compose-gen/main.go for the rationale: these defaults keep the
	// random walk's stationary probability of the unsustainable 80%-antagonist
	// (rho=1.5) ceiling at ~20%/~10%/~2% for hot/neutral/cold groups.
	hotBias := flag.Float64("hot-bias", 0.50, "ANTAGONIST_BIAS (p_up) for hot-prone backends")
	neutralBias := flag.Float64("neutral-bias", 0.425, "ANTAGONIST_BIAS (p_up) for neutral backends")
	coldBias := flag.Float64("cold-bias", 0.3, "ANTAGONIST_BIAS (p_up) for cold-prone backends")
	meanDwellMS := flag.Int("mean-dwell-ms", 2000, "mean dwell time (ms) for the antagonist random walk")
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
		algo         string
		url          string
		containerURL string // container-DNS address, used by RunLoadgen
		metricsURL   string
		serviceName  string
	}{
		{"prequal", *lbPrequal, "http://lb-prequal:8080", *lbPrequal + "/metrics", "loadbalancer-prequal"},
		{"roundrobin", *lbRR, "http://lb-roundrobin:8080", *lbRR + "/metrics", "loadbalancer-rr"},
	}

	makeEnv := func(step experiment.Step) experiment.ComposeEnv {
		return experiment.ComposeEnv{
			Backends:            *backends,
			HotBias:             *hotBias,
			NeutralBias:         *neutralBias,
			ColdBias:            *coldBias,
			MeanDwellMS:         *meanDwellMS,
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
	// Every step tears down all containers and starts fresh so backends always
	// have empty queues at measurement time. This prevents accumulated queue
	// from one step contaminating the next (queue drains slowly; the 3s settle
	// we had before was far too short when backends are near saturation).
	for _, ac := range algoConfigs {
		logger.Info("starting algorithm run", slog.String("algo", ac.algo))

		for _, step := range exp.Steps {
			logger.Info("running step",
				slog.String("algo", ac.algo),
				slog.String("label", step.Label),
				slog.Float64("rate_multiplier", step.ProbeRateMultiplier))

			if *cloudlab {
				// In CloudLab mode the LB is persistent; reconfigure it for
				// each step so probe rate and pool size actually change.
				reconfURL := fmt.Sprintf("%s/reconfigure?probe_pool_size=%d&probe_rate_multiplier=%g",
					ac.url, step.ProbePoolSize, step.ProbeRateMultiplier)
				resp, err := http.Post(reconfURL, "", nil) //nolint:noctx
				if err != nil {
					logger.Error("reconfigure failed", slog.String("url", reconfURL), slog.String("error", err.Error()))
					os.Exit(1)
				}
				resp.Body.Close()
				logger.Info("reconfigured LB",
					slog.String("algo", ac.algo),
					slog.Int("probe_pool_size", step.ProbePoolSize),
					slog.Float64("probe_rate_multiplier", step.ProbeRateMultiplier))
				// Let the probe pool repopulate at the new rate before warmup.
				time.Sleep(3 * time.Second)
			} else {
				env := makeEnv(step)
				if err := experiment.Generate(env); err != nil {
					logger.Error("compose-gen failed", slog.String("error", err.Error()))
					os.Exit(1)
				}
				// Tear down before every step so backends start with empty queues.
				if err := experiment.Down(composeFile); err != nil {
					logger.Warn("compose down before step failed (ok if nothing running)",
						slog.String("error", err.Error()))
				}
				if err := experiment.Up(composeFile, ""); err != nil {
					logger.Error("docker compose up failed", slog.String("error", err.Error()))
					os.Exit(1)
				}
				if err := experiment.WaitReady(ac.metricsURL, 60*time.Second); err != nil {
					logger.Error("LB not ready", slog.String("addr", ac.metricsURL), slog.String("error", err.Error()))
					os.Exit(1)
				}
				// Backends are always fresh; 10s lets the LB probe pool warm up.
				time.Sleep(10 * time.Second)
			}

			ctx := context.Background()

			// runLoad fires QPS for dur and returns the result. On CloudLab
			// (--cloudlab) the experiment binary runs co-located with the LB
			// and hits it directly over localhost. Otherwise it runs the
			// "loadgen" container attached to the compose network, talking to
			// the LB over container DNS — this avoids host-published ports,
			// which on Docker Desktop/WSL2 go through a slow cross-VM relay
			// that becomes the real bottleneck on long runs (and ensures dev
			// and CloudLab measure the same network path).
			runLoad := func(dur time.Duration) loadgen.Result {
				if *cloudlab {
					return loadgen.Run(ctx, loadgen.Config{URL: ac.url, QPS: *qps, Duration: dur})
				}
				res, err := experiment.RunLoadgen(composeFile, ac.containerURL, *qps, dur)
				if err != nil {
					logger.Error("loadgen container failed", slog.String("error", err.Error()))
					os.Exit(1)
				}
				return res
			}

			if *warmup > 0 {
				runLoad(*warmup)
			}
			// Let hot-backend queues drain before the measurement window.
			time.Sleep(*drain)

			res := runLoad(*duration)
			rif, rifErr := loadgen.ScrapeRIF(ac.metricsURL, ac.algo)
			if rifErr != nil {
				logger.Warn("RIF scrape failed", slog.String("error", rifErr.Error()))
			}
			if sel, selErr := loadgen.ScrapeSelections(ac.metricsURL, ac.algo); selErr != nil {
				logger.Warn("selections scrape failed", slog.String("error", selErr.Error()))
			} else {
				total := sel.Cold + sel.Hot + sel.Fallback
				hotPct := 0.0
				if total > 0 {
					hotPct = float64(sel.Hot) / float64(total) * 100
				}
				logger.Info("HCL routing split",
					slog.String("algo", ac.algo),
					slog.String("step", step.Label),
					slog.Int64("cold", sel.Cold),
					slog.Int64("hot", sel.Hot),
					slog.Int64("fallback", sel.Fallback),
					slog.Float64("hot_pct", hotPct),
				)
			}

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
		}
	}

	logger.Info("experiment complete", slog.String("output", *output))
}
