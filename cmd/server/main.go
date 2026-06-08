package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/omarshaarawi/loadbalancer/pkg/loadbalancer"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	LevelTrace = slog.Level(-8)
	LevelFatal = slog.Level(12)
)

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func main() {
	ctx := context.Background()
	port := flag.String("port", "8080", "Port to listen on")
	algorithm := flag.String("algorithm", "prequal", "Load balancing algorithm (prequal or roundrobin)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	algo := *algorithm
	if envAlgo := os.Getenv("LB_ALGORITHM"); envAlgo != "" {
		algo = envAlgo
	}

	config := &loadbalancer.Config{
		ProbeInterval:       time.Second,
		ProbeTimeout:        time.Second * 2,
		HealthCheckPath:     "/health",
		SelectionChoices:    2,
		Algorithm:           loadbalancer.Algorithm(algo),
		QRIF:                envFloat("QRIF", 0.84),
		ProbePoolSize:       envInt("PROBE_POOL_SIZE", 8),
		ProbeRateMultiplier: envFloat("PROBE_RATE_MULTIPLIER", 3.0),
	}

	lb := loadbalancer.NewLoadBalancer(config, logger)

	for i := 1; ; i++ {
		addr := os.Getenv(fmt.Sprintf("BACKEND_SERVER%d", i))
		if addr == "" {
			break
		}
		lb.AddServer(&loadbalancer.Server{
			ID:        fmt.Sprintf("server-%d", i),
			Address:   addr,
			IsHealthy: true,
		})
	}

	lb.StartProbing()

	mux := http.NewServeMux()
	mux.Handle("/", lb)
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    ":" + *port,
		Handler: mux,
	}

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		logger.Info("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Error("Server shutdown error", slog.String("error", err.Error()))
		}
	}()

	logger.Info("Load balancer starting",
		slog.String("port", *port),
		slog.String("algorithm", algo),
		slog.Float64("qrif", config.QRIF),
		slog.Int("probe_pool_size", config.ProbePoolSize),
		slog.Float64("probe_rate_multiplier", config.ProbeRateMultiplier),
	)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Log(ctx, LevelFatal, "Server error")
	}
}
