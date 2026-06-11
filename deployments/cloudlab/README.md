# CloudLab deployment

Bare-metal (no Docker) deployment: one `backend` process per port, `server`
(LB) processes for `prequal` and `roundrobin`, all driven via SSH from your
workstation. This mirrors the same antagonist/random-walk model used by the
local Docker setup (`backend/main.go`, `cmd/compose-gen`) — `ANTAGONIST_BIAS`
/ `MEAN_DWELL_MS` / `BASE_SERVICE_MS` / `CAPACITY`, with backends split into
hot/neutral/cold-prone thirds.

(The previous version of these scripts used an older `CPU_LOAD`/`--hot-fraction`
model that no longer matches `backend/main.go` and `cmd/experiment`'s actual
flags — that's been replaced.)

## Quick start

```bash
cp nodes.txt.example nodes.txt   # fill in your CloudLab node hostnames
make build-linux                 # from repo root

./deployments/cloudlab/run_experiment.sh \
    --experiment a \
    --qps 500 \
    --duration 30s --warmup 0s \
    --output results_a.csv
```

With the default `nodes.txt` (1 LB node + N backend nodes, `--backends`
unset), this runs one backend process per backend node — same topology as
before.

## Scaling to 100 backends

### 1. Node count and placement

CloudLab clusters rarely have 101 free nodes (1 LB + 100 backends). Pass
`--backends 100` with however many backend nodes you actually have —
`run_experiment.sh` packs backends onto nodes round-robin, on incrementing
ports starting at 8080:

```bash
./deployments/cloudlab/run_experiment.sh \
    --experiment a \
    --qps 5000 \
    --backends 100 \
    --duration 30s --warmup 0s \
    --output results_a_100.csv
```

With 10 backend nodes (11 total with the LB), that's 10 backend processes per
node on ports 8080-8089. The hot/neutral/cold bias groups (thirds) are
computed over all 100 backends, so each node ends up with a mix of hot,
neutral, and cold-prone backends — matching the heterogeneity the Docker setup
produces with `compose-gen -n 100`.

### 2. CPU budget per node (CAPACITY)

`CAPACITY` (default 20) represents each backend's processor-sharing capacity
in "service units" — `effectiveC = CAPACITY * (1 - antagonist_load)` and
throughput ≈ `effectiveC * 1000 / BASE_SERVICE_MS` QPS. The Docker setup gives
each backend a full dedicated `cpus: 1.0`. On bare metal, **N backend
processes on one node share that node's physical cores** — there's no cgroup
isolation here.

A `xl170`-class node has 10 cores / 20 threads. Packing 10 backends onto one
such node means each backend effectively gets ~1 core under load — roughly
equivalent to the Docker setup's `cpus: 1.0`, *if* the antagonist load isn't
simultaneously at its peak across all 10. If you pack more backends per node
than physical cores, either:
- reduce `--capacity` proportionally (e.g. half the backends/core →
  `--capacity 10`), or
- pin each backend process to its own core with `taskset -c <n>` (not done
  automatically by `run_backends.sh` — add it there if you need strict
  isolation for a paper-quality run).

Leave 1-2 cores free per node for the OS/network stack, especially on the LB
node (probing + proxying all 100 backends).

### 3. QPS scaling

The experiment design targets a fixed per-backend load ratio ρ₀. With the
defaults used by `run_experiment_a.sh` (`capacity=10`, `base-service-ms=60`):

```
C_throughput = capacity * 1000 / base_service_ms = 166.7 QPS/backend
QPS = 500, backends = 10  ->  50 QPS/backend  ->  rho_0 = 50/166.7 ~= 0.3
```

To preserve ρ₀ ≈ 0.3 with 100 backends, scale QPS proportionally:

```
QPS = 100 backends * 50 QPS/backend = 5000
```

In general: `QPS = N_BACKENDS * (rho_0 * capacity * 1000 / base_service_ms)`.

### 4. Load generator placement (`--loadgen-node`)

At 5000 QPS, the open-loop load generator (`internal/loadgen`) itself becomes
CPU-intensive — firing 5000 HTTP requests/sec, tracking percentiles, etc. If
it runs co-located with the LB (the default — `cmd/experiment --cloudlab` runs
on the LB node and hits `localhost`), it competes with the LB's proxying and
probe workers for the same cores, which can distort latency measurements.

Use `--loadgen-node HOST` to run the experiment binary on a separate,
otherwise-idle node:

```bash
./deployments/cloudlab/run_experiment.sh \
    --experiment a \
    --qps 5000 \
    --backends 100 \
    --loadgen-node node11.myexperiment.myproject.utah.cloudlab.us \
    --duration 30s --warmup 0s \
    --output results_a_100.csv
```

`HOST` doesn't need to be listed in `nodes.txt` — it's set up (binaries
copied) automatically. The script points the loadgen at the LB node's
hostname (`http://<lb-host>:8080` / `:8081`) instead of `localhost` in this
case.

### 5. Calibrate first

Before committing to QPS=5000 for a full sweep, run `--calibrate` (from the
loadgen node) to confirm the LB+backends can sustain that rate without an
unrelated bottleneck (e.g. LB connection-pool limits — see
`pkg/loadbalancer/balancer.go`'s `MaxConnsPerHost`, currently 64/backend, i.e.
6400 total for 100 backends):

```bash
./deployments/cloudlab/run_experiment.sh --calibrate --backends 100 \
    --loadgen-node node11... --output /dev/null
```

## Why no in-network-loadgen change is needed here

The Docker dev setup (`cmd/experiment` + the new `loadgen` compose service,
see top-level repo) runs the load generator *inside* the Docker bridge network
to avoid Docker Desktop/WSL2's slow host-port-forwarding relay. CloudLab is
bare metal with a real network stack and no such relay — `localhost` (or a
direct hostname, via `--loadgen-node`) behaves normally, so `--cloudlab` mode
keeps using `loadgen.Run` in-process. This also means dev and CloudLab runs
exercise the same LB code path; only the network transport differs (and on
CloudLab it's the simpler/faster one).
