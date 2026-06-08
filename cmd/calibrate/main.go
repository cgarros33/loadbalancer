// calibrate ramps QPS until p99 latency exceeds a threshold, then reports
// the saturation point. Run this once before experiments to set --qps.
//
// Usage:
//
//	calibrate --lb http://localhost:8080 --start-qps 10 --max-qps 500 \
//	          --step 10 --duration 15s --threshold 3.0
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/omarshaarawi/loadbalancer/internal/loadgen"
)

func main() {
	lb := flag.String("lb", "http://localhost:8080", "LB base URL to probe")
	startQPS := flag.Float64("start-qps", 10, "initial QPS")
	maxQPS := flag.Float64("max-qps", 500, "stop ramping at this QPS")
	step := flag.Float64("step", 10, "QPS increment per ramp step")
	duration := flag.Duration("duration", 15*time.Second, "measurement window per QPS step")
	warmupDur := flag.Duration("warmup", 5*time.Second, "warm-up before each step")
	threshold := flag.Float64("threshold", 3.0, "p99 multiplier over baseline that declares saturation")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	logger.Info("calibrating",
		slog.String("lb", *lb),
		slog.Float64("start_qps", *startQPS),
		slog.Float64("max_qps", *maxQPS),
		slog.Float64("step", *step),
		slog.Duration("duration", *duration),
		slog.Float64("saturation_threshold", *threshold),
	)

	fmt.Println("qps,p50_ms,p99_ms,p999_ms,errors")

	var baseline float64
	ctx := context.Background()

	for qps := *startQPS; qps <= *maxQPS; qps += *step {
		// warm-up
		loadgen.Run(ctx, loadgen.Config{
			URL:      *lb,
			QPS:      qps,
			Duration: *warmupDur,
		})

		res := loadgen.Run(ctx, loadgen.Config{
			URL:      *lb,
			QPS:      qps,
			Duration: *duration,
		})

		p50ms := float64(res.P50.Microseconds()) / 1000.0
		p99ms := float64(res.P99.Microseconds()) / 1000.0
		p999ms := float64(res.P999.Microseconds()) / 1000.0

		fmt.Printf("%.1f,%.3f,%.3f,%.3f,%d\n", qps, p50ms, p99ms, p999ms, res.Errors)

		if baseline == 0 && p99ms > 0 {
			baseline = p99ms
		}

		if baseline > 0 && p99ms >= baseline**threshold {
			logger.Info("saturation detected",
				slog.Float64("qps", qps),
				slog.Float64("p99_ms", p99ms),
				slog.Float64("baseline_ms", baseline),
			)
			logger.Info("recommendation",
				slog.Float64("experiment_a_qps", qps*1.5),
				slog.Float64("experiment_b_qps", qps*0.75),
			)
			os.Exit(0)
		}

		// Pause between steps to let the system settle.
		time.Sleep(2 * time.Second)
	}

	logger.Info("reached max QPS without detecting saturation",
		slog.Float64("max_qps", *maxQPS),
	)
}
