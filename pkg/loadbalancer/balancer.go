package loadbalancer

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type poolEntry struct {
	server    *Server
	rif       int32
	latency   int64
	isHealthy bool
	updatedAt time.Time
}

type LoadBalancer struct {
	servers     []*Server
	pool        []*poolEntry
	config      *Config
	stats       *Stats
	logger      *slog.Logger
	metrics     *Metrics
	mutex       sync.RWMutex
	poolMu      sync.RWMutex
	rrIndex     uint32
	probeClient *http.Client
}

func NewLoadBalancer(config *Config, logger *slog.Logger) *LoadBalancer {
	if config == nil {
		config = &Config{}
	}
	if config.Algorithm == "" {
		config.Algorithm = AlgorithmPrequal
	}
	if config.QRIF == 0 {
		config.QRIF = 0.84
	}
	if config.ProbeInterval == 0 {
		config.ProbeInterval = time.Second
	}
	if config.ProbeTimeout == 0 {
		config.ProbeTimeout = 2 * time.Second
	}
	if config.HealthCheckPath == "" {
		config.HealthCheckPath = "/health"
	}
	if config.ProbePoolSize == 0 {
		config.ProbePoolSize = 8
	}
	if config.ProbeRateMultiplier == 0 {
		config.ProbeRateMultiplier = 3.0
	}

	return &LoadBalancer{
		servers: make([]*Server, 0),
		pool:    make([]*poolEntry, 0, config.ProbePoolSize),
		config:  config,
		stats:   &Stats{},
		logger:  logger,
		metrics: NewMetrics(),
		probeClient: &http.Client{
			Timeout: config.ProbeTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// updatePool inserts or refreshes a server's entry in the bounded probe pool.
// If the pool is at capacity, the oldest entry is evicted.
func (lb *LoadBalancer) updatePool(server *Server, result *ProbeResult) {
	lb.poolMu.Lock()
	defer lb.poolMu.Unlock()

	for _, entry := range lb.pool {
		if entry.server == server {
			entry.rif = result.RIF
			entry.latency = result.Latency
			entry.isHealthy = result.IsHealthy
			entry.updatedAt = result.Timestamp
			return
		}
	}

	newEntry := &poolEntry{
		server:    server,
		rif:       result.RIF,
		latency:   result.Latency,
		isHealthy: result.IsHealthy,
		updatedAt: result.Timestamp,
	}

	if len(lb.pool) < lb.config.ProbePoolSize {
		lb.pool = append(lb.pool, newEntry)
		return
	}

	// Evict oldest entry
	oldestIdx := 0
	for i, entry := range lb.pool {
		if entry.updatedAt.Before(lb.pool[oldestIdx].updatedAt) {
			oldestIdx = i
		}
	}
	lb.pool[oldestIdx] = newEntry
}

// StartProbing warms the pool synchronously so the first request has
// fresh data, then returns. Query-triggered probing takes over from
// ServeHTTP onwards.
func (lb *LoadBalancer) StartProbing() {
	lb.probeAllServers()
}

func (lb *LoadBalancer) probeAllServers() {
	lb.mutex.RLock()
	servers := make([]*Server, len(lb.servers))
	copy(servers, lb.servers)
	lb.mutex.RUnlock()

	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(srv *Server) {
			defer wg.Done()
			lb.probeOne(srv)
		}(server)
	}
	wg.Wait()
}

func (lb *LoadBalancer) probeOne(srv *Server) {
	result := lb.probeServer(srv)

	lb.mutex.Lock()
	srv.IsHealthy = result.IsHealthy
	lb.mutex.Unlock()

	lb.updatePool(srv, result)

	algorithm := string(lb.config.Algorithm)
	if result.IsHealthy {
		lb.metrics.serverHealth.WithLabelValues(srv.ID, algorithm).Set(1)
	} else {
		lb.metrics.serverHealth.WithLabelValues(srv.ID, algorithm).Set(0)
	}
}

func (lb *LoadBalancer) probeServer(server *Server) *ProbeResult {
	start := time.Now()
	req, err := http.NewRequest("GET",
		"http://"+server.Address+lb.config.HealthCheckPath, nil)
	if err != nil {
		lb.logger.Error("Failed to create probe request",
			slog.String("server", server.ID),
			slog.String("error", err.Error()))
		return &ProbeResult{Timestamp: time.Now(), IsHealthy: false}
	}

	resp, err := lb.probeClient.Do(req)
	if err != nil {
		lb.logger.Error("Probe request failed",
			slog.String("server", server.ID),
			slog.String("error", err.Error()))
		return &ProbeResult{Timestamp: time.Now(), IsHealthy: false}
	}
	defer resp.Body.Close()

	var rif int32
	if v := resp.Header.Get("X-Requests-In-Flight"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &rif); err != nil {
			rif = atomic.LoadInt32(&server.RIF)
		}
	} else {
		rif = atomic.LoadInt32(&server.RIF)
	}

	// Use the latency reported by the server (median of recent real query
	// completions), falling back to probe RTT if the header is absent.
	// Reported latency reflects actual service time including CPU steal effects,
	// matching the paper's server-side load signal module.
	var latency int64
	if v := resp.Header.Get("X-Estimated-Latency-Ms"); v != "" {
		if val, err := strconv.ParseInt(v, 10, 64); err == nil && val > 0 {
			latency = val
		}
	}
	if latency == 0 {
		latency = time.Since(start).Milliseconds()
	}

	return &ProbeResult{
		Timestamp: time.Now(),
		RIF:       rif,
		Latency:   latency,
		IsHealthy: resp.StatusCode == http.StatusOK,
	}
}

func (lb *LoadBalancer) AddServer(server *Server) {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()
	if server.proxy == nil {
		target, _ := url.Parse("http://" + server.Address)
		proxy := httputil.NewSingleHostReverseProxy(target)
		// Each server gets its own transport to avoid sharing the global
		// DefaultTransport pool. With 2000+ probes/s + user traffic all using
		// DefaultTransport (MaxIdleConnsPerHost=2), the LB creates hundreds of
		// new TCP connections per second, congesting Docker bridge networking.
		proxy.Transport = &http.Transport{
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
		}
		server.proxy = proxy
	}
	lb.servers = append(lb.servers, server)
}

func (lb *LoadBalancer) SelectServer() *Server {
	if lb.config.Algorithm == AlgorithmRoundRobin {
		return lb.selectServerRR()
	}
	return lb.selectServerPrequal()
}

func (lb *LoadBalancer) selectServerRR() *Server {
	lb.mutex.RLock()
	defer lb.mutex.RUnlock()

	healthy := make([]*Server, 0, len(lb.servers))
	for _, s := range lb.servers {
		if s.IsHealthy {
			healthy = append(healthy, s)
		}
	}
	if len(healthy) == 0 {
		return nil
	}

	index := atomic.AddUint32(&lb.rrIndex, 1)
	return healthy[int(index-1)%len(healthy)]
}

func (lb *LoadBalancer) selectServerPrequal() *Server {
	// Snapshot the pool under read lock, then work lock-free.
	lb.poolMu.RLock()
	snapshot := make([]*poolEntry, len(lb.pool))
	copy(snapshot, lb.pool)
	lb.poolMu.RUnlock()

	algorithm := string(lb.config.Algorithm)

	// Cold-start fallback: pool not yet populated.
	if len(snapshot) == 0 {
		lb.mutex.RLock()
		defer lb.mutex.RUnlock()
		for _, s := range lb.servers {
			if s.IsHealthy {
				lb.metrics.selectionsTotal.WithLabelValues(algorithm, "fallback").Inc()
				return s
			}
		}
		return nil
	}

	healthy := make([]*poolEntry, 0, len(snapshot))
	for _, e := range snapshot {
		if e.isHealthy {
			healthy = append(healthy, e)
		}
	}
	if len(healthy) == 0 {
		return nil
	}

	// effectiveRIF = max(probe RIF, local in-flight count).
	// Probe captures load from all LB instances but may be stale.
	// Local RIF is real-time but only tracks this LB's dispatches —
	// it is a lower bound when multiple LBs share the same backends.
	// Taking the max conservatively combines both signals.
	erifs := make([]int32, len(healthy))
	for i, e := range healthy {
		local := atomic.LoadInt32(&e.server.RIF)
		if local > e.rif {
			erifs[i] = local
		} else {
			erifs[i] = e.rif
		}
	}

	threshold := rifThreshold(erifs, lb.config.QRIF)

	var cold, hot []*poolEntry
	for i, e := range healthy {
		if erifs[i] > threshold {
			hot = append(hot, e)
		} else {
			cold = append(cold, e)
		}
	}

	if len(cold) > 0 {
		lb.metrics.selectionsTotal.WithLabelValues(algorithm, "cold").Inc()
		return lowestLatency(cold).server
	}
	lb.metrics.selectionsTotal.WithLabelValues(algorithm, "hot").Inc()
	// Among hot backends use effective RIF so we pick the least loaded
	// by the same combined signal, not just the stale probe value.
	return lowestEffectiveRIF(hot, erifs, healthy).server
}

func rifThreshold(rifs []int32, qrif float64) int32 {
	sorted := make([]int32, len(rifs))
	copy(sorted, rifs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * qrif)
	return sorted[idx]
}

func lowestLatency(entries []*poolEntry) *poolEntry {
	min := entries[0].latency
	for _, e := range entries[1:] {
		if e.latency < min {
			min = e.latency
		}
	}
	// Collect all entries tied at the minimum and pick randomly among them.
	// In production, backends have natural latency variation so ties are rare.
	// In simulation, deterministic fallback values create exact ties; random
	// tie-breaking distributes load instead of always pinning the first entry.
	tied := entries[:0:0]
	for _, e := range entries {
		if e.latency == min {
			tied = append(tied, e)
		}
	}
	if len(tied) == 1 {
		return tied[0]
	}
	return tied[rand.Intn(len(tied))]
}

func lowestRIF(entries []*poolEntry) *poolEntry {
	best := entries[0]
	for _, e := range entries[1:] {
		if e.rif < best.rif {
			best = e
		}
	}
	return best
}

// lowestEffectiveRIF picks the hot entry with the smallest effective RIF.
// hot and erifs are parallel slices; healthy maps erifs indices back to entries.
func lowestEffectiveRIF(hot []*poolEntry, erifs []int32, healthy []*poolEntry) *poolEntry {
	// Build a map from poolEntry pointer to its effective RIF index.
	idx := make(map[*poolEntry]int32, len(healthy))
	for i, e := range healthy {
		idx[e] = erifs[i]
	}
	best := hot[0]
	for _, e := range hot[1:] {
		if idx[e] < idx[best] {
			best = e
		}
	}
	return best
}

func (lb *LoadBalancer) fireProbes() {
	lb.mutex.RLock()
	servers := make([]*Server, len(lb.servers))
	copy(servers, lb.servers)
	lb.mutex.RUnlock()

	if len(servers) == 0 {
		return
	}

	rate := lb.config.ProbeRateMultiplier
	whole := int(rate)
	frac := rate - float64(whole)

	count := whole
	if frac > 0 && rand.Float64() < frac {
		count++
	}

	for i := 0; i < count; i++ {
		srv := servers[rand.Intn(len(servers))]
		go lb.probeOne(srv)
	}
}

func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&lb.stats.TotalRequests, 1)

	lb.fireProbes()

	server := lb.SelectServer()
	if server == nil {
		lb.logger.Error("No available servers")
		atomic.AddUint64(&lb.stats.FailedRequests, 1)
		http.Error(w, "No available servers", http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	lb.forwardRequest(server, w, r)
	duration := time.Since(start)

	algorithm := string(lb.config.Algorithm)
	lb.metrics.requestDuration.WithLabelValues(algorithm).Observe(duration.Seconds())
	atomic.AddUint64(&lb.stats.SuccessfulRequests, 1)
}

func (lb *LoadBalancer) forwardRequest(server *Server, w http.ResponseWriter, r *http.Request) {
	algorithm := string(lb.config.Algorithm)
	rif := atomic.AddInt32(&server.RIF, 1)
	lb.metrics.activeRequests.WithLabelValues(algorithm).Inc()
	lb.metrics.rifAtDispatch.WithLabelValues(algorithm).Observe(float64(rif))

	defer func() {
		atomic.AddInt32(&server.RIF, -1)
		lb.metrics.activeRequests.WithLabelValues(algorithm).Dec()
		lb.metrics.serverRIF.WithLabelValues(server.ID, algorithm).Set(
			float64(atomic.LoadInt32(&server.RIF)),
		)
	}()

	server.proxy.ServeHTTP(w, r)
}
