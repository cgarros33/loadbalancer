package main

import (
	"fmt"
	"hash/fnv"
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

// antagonistLevels are the discrete states of the antagonist random walk,
// expressed as a percentage of this backend's CAPACITY consumed by
// co-located jobs. The walk only steps between adjacent levels, with a
// floor at 20% and a ceiling at 80% (no antagonist ever leaves the backend
// completely idle, and none ever fully starves it).
var antagonistLevels = []int{20, 35, 50, 65, 80}

// neutralLevelIdx is the starting index (50%) and the index used for the
// latency-tracker fallback before any real samples exist.
const neutralLevelIdx = 2

// dynamicAntagonist models "whatever we happen to encounter in the wild"
// (the paper's description of antagonist traffic): a per-backend, time-
// varying contention level that the load balancer never observes directly,
// only through its effect on RIF and latency.
type dynamicAntagonist struct {
	idx atomic.Int32
}

func (d *dynamicAntagonist) level() int {
	return antagonistLevels[d.idx.Load()]
}

// run advances the random walk forever, one step per dwell period. pUp is
// the probability of stepping toward higher antagonist load at each
// transition (heterogeneous across backends so some run hotter on average
// than others, without any of them being statically pinned). dwell periods
// are drawn from an exponential distribution with mean meanDwell: memoryless,
// so the walk has no detectable periodicity, and short enough relative to the
// 30s measurement window that many transitions occur per step.
func (d *dynamicAntagonist) run(rng *rand.Rand, pUp float64, meanDwell time.Duration) {
	idx := int32(neutralLevelIdx)
	d.idx.Store(idx)
	for {
		dwell := time.Duration(rng.ExpFloat64() * float64(meanDwell))
		time.Sleep(dwell)
		if rng.Float64() < pUp {
			if idx < int32(len(antagonistLevels)-1) {
				idx++
			}
		} else {
			if idx > 0 {
				idx--
			}
		}
		d.idx.Store(idx)
	}
}

// computeEffective derives this backend's instantaneous effective capacity,
// per-request service time, and queue depth from its current antagonist
// level. The antagonist consumes antagonistPct% of CAPACITY, so requests run
// on a proportionally smaller share of CPU and take longer — even below
// saturation — exactly as shared-resource contention (cache, memory
// bandwidth, context switches) would in practice.
//
//	effectiveServiceMS = BASE_MS × capacity / effectiveC
//
// preserves saturation throughput (= effectiveC × 1000 / BASE_MS) while
// raising per-request latency and RIF as antagonist load rises. Queue depth
// is 20× saturation throughput, giving generous backlog headroom before
// requests are shed (shedding in practice happens via the loadgen's 5s
// deadline / context cancellation, not queue depth).
func computeEffective(capacity, baseServiceMS, antagonistPct int) (effectiveC int, effectiveServiceMS float64, queueDepth int) {
	effectiveC = capacity - capacity*antagonistPct/100
	if effectiveC < 1 {
		effectiveC = 1
	}
	effectiveServiceMS = float64(baseServiceMS) * float64(capacity) / float64(effectiveC)
	saturationQPS := effectiveC * 1000 / baseServiceMS
	queueDepth = saturationQPS * 20
	return
}

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

	// ANTAGONIST_BIAS is p_up: the probability that the random walk steps
	// toward a higher antagonist level at each transition. 0.5 is neutral
	// (stationary mean ≈ 50%); >0.5 biases this backend hot-prone, <0.5
	// cold-prone. Backends are nominally identical hardware — only this
	// propensity differs, modelling heterogeneous "neighbours" per backend.
	antagonistBias := 0.5
	if v := os.Getenv("ANTAGONIST_BIAS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			antagonistBias = f
		}
	}

	// MEAN_DWELL_MS is the mean time spent at each antagonist level before
	// transitioning. Default 2s: long enough relative to a single request's
	// service time that a request experiences consistent conditions
	// end-to-end, short enough relative to the 30s measurement window that
	// many transitions occur per step.
	meanDwellMS := 2000
	if v := os.Getenv("MEAN_DWELL_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			meanDwellMS = val
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

	sampleServiceMS := func(effectiveServiceMS float64) float64 {
		if jitter == 0 {
			return effectiveServiceMS
		}
		s := effectiveServiceMS + jitter*effectiveServiceMS*rand.NormFloat64()
		if s < 1 {
			s = 1
		}
		return s
	}

	// Semaphore at capacity: all threads available, each running at reduced
	// speed depending on the current antagonist level.
	sem := make(chan struct{}, capacity)

	// Tracker window: short enough to detect sudden load changes quickly,
	// long enough to accumulate a stable median. Configurable via
	// TRACKER_WINDOW_MS; defaults to 500ms.
	trackerWindow := 500 * time.Millisecond
	if v := os.Getenv("TRACKER_WINDOW_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			trackerWindow = time.Duration(ms) * time.Millisecond
		}
	}
	_, neutralServiceMS, _ := computeEffective(capacity, baseServiceMS, antagonistLevels[neutralLevelIdx])
	tracker := newLatencyTracker(trackerWindow, int64(neutralServiceMS))

	// Seed this backend's antagonist random walk independently of every
	// other backend (and across restarts) using its server ID + wall clock.
	h := fnv.New64a()
	h.Write([]byte(serverID))
	seed := int64(h.Sum64()) ^ time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	antagonist := &dynamicAntagonist{}
	go antagonist.run(rng, antagonistBias, time.Duration(meanDwellMS)*time.Millisecond)

	log.Printf("server %s starting on :%s — capacity=%d base_ms=%d antagonist_bias=%.2f mean_dwell_ms=%d jitter=%.1f",
		serverID, port, capacity, baseServiceMS, antagonistBias, meanDwellMS, jitter)
	for _, lvl := range antagonistLevels {
		ec, ems, qd := computeEffective(capacity, baseServiceMS, lvl)
		log.Printf("  antagonist=%d%% -> effective_c=%d effective_ms=%.0f queue_depth=%d", lvl, ec, ems, qd)
	}

	// serve processes one request and returns (http status, RIF at arrival).
	// It respects r.Context() so that when the upstream client (the LB proxy)
	// cancels the request (e.g. the loadgen's 5 s timeout fires), the goroutine
	// releases its semaphore slot immediately instead of lingering as a zombie.
	// Without this, cancelled goroutines block on sem indefinitely, starving
	// legitimate requests in subsequent steps.
	//
	// effectiveC/effectiveServiceMS/queueDepth are recomputed from the
	// antagonist level AT ARRIVAL TIME and held fixed for the lifetime of the
	// request. This is what produces the "drain effect": a request dispatched
	// while the antagonist is calm keeps its short timer even if the
	// antagonist spikes mid-flight, and vice versa — giving probes a memory
	// of recent conditions without any synthetic RIF baseline.
	serve := func(r *http.Request, path string) (int, int32, int, float64) {
		level := antagonist.level()
		effectiveC, effectiveServiceMS, queueDepth := computeEffective(capacity, baseServiceMS, level)

		arrivalRIF := rif.Add(1)
		defer rif.Add(-1)

		if int(arrivalRIF) > queueDepth {
			return http.StatusServiceUnavailable, arrivalRIF, level, effectiveServiceMS
		}

		select {
		case sem <- struct{}{}:
		case <-r.Context().Done():
			return http.StatusServiceUnavailable, arrivalRIF, level, effectiveServiceMS
		}
		defer func() { <-sem }()

		sample := sampleServiceMS(effectiveServiceMS)
		if debug {
			log.Printf("path=%s arrival_rif=%d antagonist=%d%% effective_c=%d effective_ms=%.0f sample_ms=%.2f",
				path, arrivalRIF, level, effectiveC, effectiveServiceMS, sample)
		}
		timer := time.NewTimer(time.Duration(sample * float64(time.Millisecond)))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return http.StatusServiceUnavailable, arrivalRIF, level, effectiveServiceMS
		}
		return http.StatusOK, arrivalRIF, level, effectiveServiceMS
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status, arrivalRIF, level, effectiveServiceMS := serve(r, "/")
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
<p>Antagonist: %d%%  effective_ms: %.0f</p>
</body></html>`, serverID, elapsedMs, arrivalRIF, level, effectiveServiceMS)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		currentRIF := rif.Load()
		estimatedMs, hasReal := tracker.medianAtRIF(currentRIF)
		// Little's Law floor: when no real samples exist near this RIF level
		// (e.g. sudden load spike with no completions yet), estimate expected
		// wait from current queue depth at the current antagonist level. Real
		// measurements take priority.
		if !hasReal {
			level := antagonist.level()
			effectiveC, effectiveServiceMS, _ := computeEffective(capacity, baseServiceMS, level)
			if ll := int64(currentRIF) * int64(effectiveServiceMS) / int64(effectiveC); ll > estimatedMs {
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
