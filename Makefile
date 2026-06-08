GOOS_LOCAL := $(shell go env GOOS)
GOARCH_LOCAL := $(shell go env GOARCH)

# ── build ──────────────────────────────────────────────────────────────────────
.PHONY: build
build:
	go build -o bin/server      ./cmd/server
	go build -o bin/backend     ./backend
	go build -o bin/experiment  ./cmd/experiment
	go build -o bin/calibrate   ./cmd/calibrate
	go build -o bin/compose-gen ./cmd/compose-gen
	go build -o bin/loadgen     ./cmd/loadgen

# Cross-compile for CloudLab (linux/amd64) regardless of local OS.
.PHONY: build-linux
build-linux:
	mkdir -p bin/linux
	GOOS=linux GOARCH=amd64 go build -o bin/linux/server      ./cmd/server
	GOOS=linux GOARCH=amd64 go build -o bin/linux/backend     ./backend
	GOOS=linux GOARCH=amd64 go build -o bin/linux/experiment  ./cmd/experiment
	GOOS=linux GOARCH=amd64 go build -o bin/linux/calibrate   ./cmd/calibrate

# ── docker compose ─────────────────────────────────────────────────────────────
.PHONY: up
up:
	docker compose up -d --build

.PHONY: down
down:
	docker compose down

.PHONY: gen-compose
gen-compose:
	go run ./cmd/compose-gen $(COMPOSE_ARGS)

# ── calibration ────────────────────────────────────────────────────────────────
# Run after `make up`. Adjust --max-qps to suit your machine.
.PHONY: calibrate
calibrate: build
	./bin/calibrate \
		--lb http://localhost:8080 \
		--start-qps 10 \
		--max-qps 500 \
		--step 10 \
		--duration 15s \
		$(CALIBRATE_ARGS)

# ── experiments ────────────────────────────────────────────────────────────────
QPS          ?= 100
BACKENDS     ?= 10
CPU_LOAD     ?= 50
HOT_FRACTION ?= 0.0
HOT_CPU_LOAD ?= 80
DURATION     ?= 30s
WARMUP       ?= 10s
OUTPUT       ?= results.csv

.PHONY: experiment-a
experiment-a: build
	./bin/experiment \
		--experiment a \
		--qps $(QPS) \
		--backends $(BACKENDS) \
		--cpu-load $(CPU_LOAD) \
		--hot-fraction $(HOT_FRACTION) \
		--hot-cpu-load $(HOT_CPU_LOAD) \
		--duration $(DURATION) \
		--warmup $(WARMUP) \
		--output $(OUTPUT) \
		$(EXPERIMENT_ARGS)

.PHONY: experiment-b
experiment-b: build
	./bin/experiment \
		--experiment b \
		--qps $(QPS) \
		--backends $(BACKENDS) \
		--cpu-load $(CPU_LOAD) \
		--hot-fraction $(HOT_FRACTION) \
		--hot-cpu-load $(HOT_CPU_LOAD) \
		--duration $(DURATION) \
		--warmup $(WARMUP) \
		--output $(OUTPUT) \
		$(EXPERIMENT_ARGS)

# ── cloudlab ───────────────────────────────────────────────────────────────────
NODES_FILE ?= deployments/cloudlab/nodes.txt

.PHONY: cloudlab-setup
cloudlab-setup: build-linux
	@for node in $$(grep -v '^\s*#' $(NODES_FILE) | grep -v '^\s*$$'); do \
		./deployments/cloudlab/setup.sh "$$node" & \
	done; wait

.PHONY: cloudlab-calibrate
cloudlab-calibrate:
	./deployments/cloudlab/run_experiment.sh \
		--nodes $(NODES_FILE) \
		--calibrate \
		$(CLOUDLAB_ARGS)

.PHONY: cloudlab-experiment-a
cloudlab-experiment-a:
	./deployments/cloudlab/run_experiment.sh \
		--nodes $(NODES_FILE) \
		--experiment a \
		--qps $(QPS) \
		--cpu-load $(CPU_LOAD) \
		--hot-fraction $(HOT_FRACTION) \
		--hot-cpu-load $(HOT_CPU_LOAD) \
		--duration $(DURATION) \
		--warmup $(WARMUP) \
		--output $(OUTPUT) \
		$(CLOUDLAB_ARGS)

.PHONY: cloudlab-experiment-b
cloudlab-experiment-b:
	./deployments/cloudlab/run_experiment.sh \
		--nodes $(NODES_FILE) \
		--experiment b \
		--qps $(QPS) \
		--cpu-load $(CPU_LOAD) \
		--hot-fraction $(HOT_FRACTION) \
		--hot-cpu-load $(HOT_CPU_LOAD) \
		--duration $(DURATION) \
		--warmup $(WARMUP) \
		--output $(OUTPUT) \
		$(CLOUDLAB_ARGS)

# ── plotting ───────────────────────────────────────────────────────────────────
PLOT_INPUT ?= results.csv
PLOT_DIR   ?= plots

.PHONY: plot-a
plot-a:
	python3 scripts/plot.py --input $(PLOT_INPUT) --experiment a --output $(PLOT_DIR)

.PHONY: plot-b
plot-b:
	python3 scripts/plot.py --input $(PLOT_INPUT) --experiment b --output $(PLOT_DIR)

.PHONY: plot
plot: plot-a plot-b

# ── test ───────────────────────────────────────────────────────────────────────
.PHONY: test
test:
	go test ./...

.PHONY: clean
clean:
	rm -rf bin/ plots/
