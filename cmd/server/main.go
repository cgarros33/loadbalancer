package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omarshaarawi/loadbalancer/internal/config"
	"github.com/omarshaarawi/loadbalancer/pkg/loadbalancer"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	LevelTrace = slog.Level(-8)
	LevelFatal = slog.Level(12)
)

func main() {
	ctx := context.Background()
	configPath := flag.String("config", "loadbalancer.json", "Path to configuration file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		logger.Error("Failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Allow LB_ALGORITHM env var to override (for docker-compose dual-LB setup)
	algo := cfg.LoadBalancer.Algorithm
	if envAlgo := os.Getenv("LB_ALGORITHM"); envAlgo != "" {
		algo = envAlgo
	}

	lbConfig := &loadbalancer.Config{
		ProbeInterval:    cfg.ProbeInterval(),
		ProbeTimeout:     cfg.ProbeTimeout(),
		HealthCheckPath:  cfg.Prequal.HealthCheckPath,
		SelectionChoices: cfg.Prequal.SelectionChoices,
		Algorithm:        loadbalancer.Algorithm(algo),
		QRIF:             cfg.Prequal.QRIF,
	}

	lb := loadbalancer.NewLoadBalancer(lbConfig, logger)

	logger.Info("Load balancer configured",
		slog.String("algorithm", algo),
		slog.Int("servers", len(cfg.Servers)),
		slog.Int("selection_choices", cfg.Prequal.SelectionChoices),
		slog.Float64("qrif", cfg.Prequal.QRIF),
	)

	// Dynamically add servers from config — no hardcoded list
	for _, srv := range cfg.Servers {
		lb.AddServer(&loadbalancer.Server{
			ID:        srv.ID,
			Address:   srv.Address,
			IsHealthy: true,
		})
		logger.Info("Added backend server",
			slog.String("id", srv.ID),
			slog.String("address", srv.Address),
		)
	}

	lb.StartProbing()

	mux := http.NewServeMux()
	mux.Handle("/", lb)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	port := cfg.LoadBalancer.Port
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout(),
		WriteTimeout: cfg.WriteTimeout(),
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

	logger.Info("Starting server on port " + port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Log(ctx, LevelFatal, "Server error")
	}
}
