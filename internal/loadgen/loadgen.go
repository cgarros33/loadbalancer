// Package loadgen provides an open-loop HTTP load generator.
// Requests are fired on a fixed-rate ticker regardless of outstanding
// responses, matching the paper's load model.
package loadgen

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Config controls a single load generation run.
type Config struct {
	URL      string
	QPS      float64
	Duration time.Duration
	// MaxConcurrent caps in-flight requests. 0 = unlimited.
	MaxConcurrent int
}

// Result holds latency percentiles and counters collected during a run.
type Result struct {
	P50, P99, P999 time.Duration
	Total          int64
	Errors         int64
}

// Run fires requests at Config.QPS for Config.Duration and returns
// aggregate latency statistics. ctx cancellation stops the run early.
func Run(ctx context.Context, cfg Config) Result {
	if cfg.QPS <= 0 || cfg.Duration <= 0 {
		return Result{}
	}

	interval := time.Duration(float64(time.Second) / cfg.QPS)

	var (
		mu       sync.Mutex
		latencies []time.Duration
		errors   atomic.Int64
		total    atomic.Int64
	)

	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}

	client := &http.Client{Timeout: 5 * time.Second}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	deadline := time.Now().Add(cfg.Duration)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case t := <-ticker.C:
			if t.After(deadline) {
				break loop
			}
			if sem != nil {
				select {
				case sem <- struct{}{}:
				default:
					// skip this tick if at concurrency limit
					continue
				}
			}
			go func() {
				if sem != nil {
					defer func() { <-sem }()
				}
				start := time.Now()
				resp, err := client.Get(cfg.URL)
				elapsed := time.Since(start)
				total.Add(1)
				if err != nil || resp.StatusCode >= 500 {
					errors.Add(1)
					if err == nil {
						resp.Body.Close()
					}
					return
				}
				resp.Body.Close()
				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}()
		}
	}
	// drain in-flight requests
	if sem != nil {
		for i := 0; i < cap(sem); i++ {
			sem <- struct{}{}
		}
	}
	// small settle window for last goroutines
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	res := Result{
		Total:  total.Load(),
		Errors: errors.Load(),
	}
	if len(latencies) == 0 {
		return res
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	res.P50 = latencies[percentileIdx(len(latencies), 0.50)]
	res.P99 = latencies[percentileIdx(len(latencies), 0.99)]
	res.P999 = latencies[percentileIdx(len(latencies), 0.999)]
	return res
}

func percentileIdx(n int, p float64) int {
	idx := int(float64(n-1) * p)
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}
