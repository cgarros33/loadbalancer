package loadbalancer

import (
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type Server struct {
	ID        string
	Address   string
	RIF       int32
	Latency   int64
	IsHealthy bool
	LastProbe time.Time
	proxy     *httputil.ReverseProxy
}

func newServer(id, address string) *Server {
	target, _ := url.Parse("http://" + address)
	p := httputil.NewSingleHostReverseProxy(target)
	return &Server{
		ID:        id,
		Address:   address,
		IsHealthy: true,
		proxy:     p,
	}
}

type ProbeResult struct {
	Timestamp time.Time
	RIF       int32
	Latency   int64
	IsHealthy bool
}

type Algorithm string

const (
	AlgorithmPrequal     Algorithm = "prequal"
	AlgorithmRoundRobin  Algorithm = "roundrobin"
)

type Config struct {
	ProbeInterval       time.Duration
	ProbeTimeout        time.Duration
	HealthCheckPath     string
	SelectionChoices    int
	Algorithm           Algorithm
	QRIF                float64
	ProbePoolSize       int
	ProbeRateMultiplier float64
}

type Stats struct {
	TotalRequests      uint64
	SuccessfulRequests uint64
	FailedRequests     uint64
	AverageLatency     float64
	mutex              sync.RWMutex
}
