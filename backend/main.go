package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var rif atomic.Int32

// rifBucket maps a RIF value to a log2 bucket index (0–9) for latency lookup.
// bucket 0 = RIF 0, bucket k = RIF in [2^(k-1), 2^k − 1].
func rifBucket(rif int32) int {
	if rif <= 0 {
		return 0
	}
	b := 0
	v := rif
	for v > 0 {
		v >>= 1
		b++
	}
	if b > 9 {
		return 9
	}
	return b
}

// latencyTracker records actual request latencies tagged with the RIF at
// arrival, mirroring the paper's server-side load signal: "when a query
// finishes, we record its latency tagged by the RIF counter when it arrived;
// when a probe arrives, we consult recent latency values at (or near) the
// current RIF and report the median."
type latencyTracker struct {
	mu       sync.Mutex
	samples  []latencySample
	window   time.Duration
	fallback int64 // ms — used when no samples exist yet
}

type latencySample struct {
	ms  int64
	rif int32
	at  time.Time
}

func newLatencyTracker(window time.Duration, fallbackMs int64) *latencyTracker {
	return &latencyTracker{window: window, fallback: fallbackMs}
}

func (t *latencyTracker) record(ms int64, arrivalRIF int32) {
	t.mu.Lock()
	t.samples = append(t.samples, latencySample{ms, arrivalRIF, time.Now()})
	t.mu.Unlock()
}

// medianAtRIF prunes expired samples then returns the median latency for
// samples whose arrival RIF falls in the same log2 bucket as currentRIF.
// If that bucket has fewer than 3 samples the search expands to adjacent
// buckets; if still empty it falls back to all samples, then to fallback.
// The second return value is true when at least one sample was found
// within radius ≤ 1 of the target bucket (i.e. real data for this RIF level).
func (t *latencyTracker) medianAtRIF(currentRIF int32) (int64, bool) {
	t.mu.Lock()
	cutoff := time.Now().Add(-t.window)
	i := 0
	for i < len(t.samples) && t.samples[i].at.Before(cutoff) {
		i++
	}
	t.samples = t.samples[i:]
	if len(t.samples) == 0 {
		t.mu.Unlock()
		return t.fallback, false
	}

	target := rifBucket(currentRIF)
	var vals []int64
	var foundReal bool
	for radius := 0; radius <= 9; radius++ {
		vals = vals[:0]
		lo, hi := target-radius, target+radius
		for _, s := range t.samples {
			b := rifBucket(s.rif)
			if b >= lo && b <= hi {
				vals = append(vals, s.ms)
			}
		}
		if len(vals) >= 3 {
			foundReal = radius <= 1
			break
		}
	}
	t.mu.Unlock()

	if len(vals) == 0 {
		return t.fallback, false
	}
	sort.Slice(vals, func(a, b int) bool { return vals[a] < vals[b] })
	return vals[len(vals)/2], foundReal
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	serverID := os.Getenv("SERVER_ID")
	if serverID == "" {
		serverID = "unknown"
	}

	antagonistLoad := 0
	if v := os.Getenv("ANTAGONIST_LOAD"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			antagonistLoad = val
		}
	}

	baseServiceMS := 5
	if v := os.Getenv("BASE_SERVICE_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			baseServiceMS = val
		}
	}

	capacity := 20
	if v := os.Getenv("CAPACITY"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			capacity = val
		}
	}

	debug := os.Getenv("DEBUG") == "1"

	// The antagonist consumes antagonistLoad% of the server's CPU allocation,
	// causing every request to run on a proportionally smaller share of CPU
	// and therefore take longer — even below saturation. Shared resources
	// (L3 cache, memory bandwidth, context switches) are not fully isolated,
	// so hot backends genuinely respond slower per-request than cold ones
	// regardless of queue depth.
	//
	// All capacity threads remain available (semaphore = capacity) but each
	// progresses at reduced speed:
	//
	//   effectiveServiceMS = BASE_MS × capacity / effectiveC
	//
	// This preserves saturation throughput (= effectiveC × 1000 / BASE_MS)
	// while correctly raising per-request latency and RIF on hot backends.
	// Little's law: RIF = QPS × effectiveServiceMS, so hot backends naturally
	// show higher RIF at the same incoming rate.
	//
	// Queue depth is capped at 5× saturation throughput, giving ~5 s of
	// backlog before requests are shed — matching the paper's 5 s RPC deadline
	// as the point at which further queuing yields only deadline-exceeded
	// errors rather than useful capacity.

	effectiveC := capacity - int(float64(capacity)*float64(antagonistLoad)/100.0)
	if effectiveC < 1 {
		effectiveC = 1
	}

	effectiveServiceMS := float64(baseServiceMS) * float64(capacity) / float64(effectiveC)

	saturationQPS := effectiveC * 1000 / baseServiceMS
	queueDepth := saturationQPS * 20
	if v := os.Getenv("QUEUE_DEPTH"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			queueDepth = val
		}
	}

	// jitter=1.0: the paper draws query cost from a normal distribution whose
	// standard deviation equals its mean (truncated at zero), so std = mean.
	jitter := 1.0
	if v := os.Getenv("SERVICE_JITTER"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			jitter = f
		}
	}

	sampleServiceMS := func() float64 {
		if jitter == 0 {
			return effectiveServiceMS
		}
		s := effectiveServiceMS + jitter*effectiveServiceMS*rand.NormFloat64()
		if s < 1 {
			s = 1
		}
		return s
	}

	// Semaphore at capacity: all threads available, each running at reduced speed.
	sem := make(chan struct{}, capacity)

	// Tracker window of 2 s matches the probe pool's staleness horizon.
	// Fallback = effectiveServiceMS ensures idle hot backends always report
	// higher latency than cold ones, preventing routing oscillation.
	tracker := newLatencyTracker(2*time.Second, int64(effectiveServiceMS))

	log.Printf("server %s starting on :%s — capacity=%d antagonist=%d%% effective_c=%d effective_ms=%.0f sat_qps=%d queue_depth=%d jitter=%.1f",
		serverID, port, capacity, antagonistLoad, effectiveC, effectiveServiceMS, saturationQPS, queueDepth, jitter)

	// serve processes one request and returns (http status, RIF at arrival).
	serve := func(path string) (int, int32) {
		arrivalRIF := rif.Add(1)
		defer rif.Add(-1)

		if int(arrivalRIF) > queueDepth {
			return http.StatusServiceUnavailable, arrivalRIF
		}

		sem <- struct{}{}
		defer func() { <-sem }()

		sample := sampleServiceMS()
		if debug {
			log.Printf("path=%s arrival_rif=%d capacity=%d effective_ms=%.0f sample_ms=%.2f",
				path, arrivalRIF, capacity, effectiveServiceMS, sample)
		}
		time.Sleep(time.Duration(sample * float64(time.Millisecond)))
		return http.StatusOK, arrivalRIF
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status, arrivalRIF := serve("/")
		elapsedMs := time.Since(start).Milliseconds()

		if status == http.StatusOK {
			tracker.record(elapsedMs, arrivalRIF)
		}

		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Served-By", serverID)
		w.Header().Set("X-Requests-In-Flight", strconv.Itoa(int(rif.Load())))
		if status != http.StatusOK {
			http.Error(w, "queue full", status)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Backend</title></head>
<body>
<h1>Backend: %s</h1>
<p>Processed in %dms (arrival RIF %d)</p>
<p>Antagonist: %d%%  effective_C: %d  effective_ms: %.0f</p>
</body></html>`, serverID, elapsedMs, arrivalRIF, antagonistLoad, effectiveC, effectiveServiceMS)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		currentRIF := rif.Load()
		estimatedMs, hasReal := tracker.medianAtRIF(currentRIF)
		// Little's Law floor: when no real samples exist near this RIF level
		// (e.g. sudden load spike with no completions yet), estimate expected
		// wait from current queue depth. Real measurements take priority.
		if !hasReal {
			if ll := int64(float64(currentRIF) * effectiveServiceMS / float64(capacity)); ll > estimatedMs {
				estimatedMs = ll
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Requests-In-Flight", strconv.Itoa(int(currentRIF)))
		w.Header().Set("X-Estimated-Latency-Ms", strconv.FormatInt(estimatedMs, 10))
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"healthy","server_id":"%s"}`, serverID)
	})

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
