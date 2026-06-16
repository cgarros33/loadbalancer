# Replicating: "Prequal: Load is not what you should balance: Introducing Prequal"

**Team Members:**  
Hannah Goldstein (hannah.barbosa@mail.polimi.it);  
Nico Koron (nicolas.koron@mail.polimi.it);  
Celestino Garrós (celestino.garros@mail.polimi.it)

---

**Source Paper:**
Bartek Wydrowski, Robert Kleinberg, Stephen M. Rumble, Aaron Archer: Load is not what you should balance: Introducing Prequal. In Proceedings of the 21st USENIX Symposium on Networked Systems Design and Implementation (NSDI ’24), USENIX Association, 2024.


**Project:**
https://github.com/omarshaarawi/loadbalancer.git 

---

# 1. Introduction

Large-scale online services route each incoming query to one of many interchangeable
replicas of a backend service. The choice of replica has a direct effect on tail
latency and error rates: a query sent to an overloaded or otherwise degraded replica
can take orders of magnitude longer than the same query sent to a healthy one. The
paper argues that the metrics conventionally used to make this choice — CPU
utilization, server-reported "load", or simple request-in-flight counts on their
own — correlate poorly with the latency a query will actually experience, because
replicas in a real datacenter are heterogeneous: different hardware generations,
co-located "antagonist" jobs competing for CPU/cache/network, and request-cost
variance all change a replica's effective capacity in ways that are not visible to
the client choosing where to send the next query.

Prequal's approach is to have each client directly **probe** a random sample of
replicas for two real-time signals — **R**equests-**I**n-**F**light (RIF) and a
recent **observed latency** — and maintain a small, continuously refreshed **probe
pool** of these responses. When a query arrives, the client picks a replica from the
pool using the **HCL** (Hot/Cold-Latency) rule: replicas whose RIF exceeds a
threshold (`QRIF`) are classified "hot"; among "cold" replicas the client picks the
one with the lowest recent latency, falling back to a latency-based choice among hot
replicas only if the whole pool is hot. The probing rate and pool size are tunable
parameters with a clear cost/benefit trade-off, which the paper studies explicitly.

The main contributions are: (1) the probe-pool maintenance protocol (bounded size,
age-based eviction, configurable probing rate); (2) the HCL replica-selection rule
combining RIF and latency signals; and (3) a large-scale evaluation — including a
production deployment at Google — showing that Prequal reduces tail latency and
error rates relative to simpler baselines (Round Robin, Least-Loaded, Power-of-Two-
Choices, C3, etc.).

# 2. Selected Result

We reproduce two analyses centered on Prequal's two tunable knobs — **probing rate**
and **probe pool size** — comparing our implementation of the HCL selection rule
("Prequal") against Round Robin ("RR") as the baseline.

- **Experiment A** replicates **Figure 8**: the probe-rate sweep. The probe pool
  size is held fixed (P=8) and the probe rate is varied as a multiple of the query
  rate, under a workload designed to push the system toward the paper's stated worst
  case of "roughly 1.5x our CPU allocation". The paper's headline claim is:

  > "Prequal is fairly insensitive to the probing rate until we drop below one probe
  > per query, at which point the negative effects become significant... the tail
  > RIF distributions jump visibly, and this is echoed by both latency quantiles."

- **Experiment B** sweeps the **probe pool size** P at a fixed probe rate (3x query
  rate), exploring the pool-size trade-off the paper discusses in Section 5 (default
  pool size 16, capped, with the explicit assumption that "each client's probe pool
  represents only a small random subset of replicas").

For both experiments we measure end-to-end p99/p99.9 request latency and the
selected replica's RIF percentiles (p50/p75/p99), at fleet sizes of 10, 50 and 100
backends.

# 3. Environment Setup

**Hardware Environment:**
A single Windows host running Docker Desktop on WSL2 (kernel
`5.15.167.4-microsoft-standard-WSL2`), 12 vCPUs, 15GB RAM. All services (load
balancer, backends, load generator, Prometheus/Grafana) run as containers on a
single Docker Compose network on this one host — there is no real network between
"replicas" as in the paper's datacenter deployment.

**Software Environment:**
Go 1.24 (module), built with the go1.26.3 toolchain; Docker 28.1.1 / Docker Compose
v2.35.1. No public artifact from the paper was available — the load balancer, HCL
selection rule, probe pool, backend simulator, and load generator are all
reimplemented from scratch in this repository (commit history on `main`).

**Configuration Parameters:**

Common to both experiments:
- `capacity=10` (nominal concurrency slots per backend), `base-service-ms=60` →
  `C_throughput = 166.7` QPS/backend.
- Query load `QPS = 50 * backends` (≈30% of `C_throughput` per backend at the
  antagonist floor).
- Per-backend **antagonist** process: a 5-state random walk over
  `{20, 35, 50, 65, 80}%` of `capacity`, mean dwell time 2000ms. Backends are split
  into three groups — hot-bias=0.50, neutral-bias=0.425, cold-bias=0.3 — so the
  worst antagonist state (`80%`, giving `effectiveC=2` and `rho=1.5`, i.e. the
  paper's "1.5x allocation" worst case) is visited ~20% of the time for hot
  backends, ~10% for neutral, ~2% for cold.
- `QRIF=0.70` (HCL hot/cold RIF threshold).
- Background probe-worker pool: `probeWorkers=150`, `probeQueueSize=300`.

Experiment-specific:
- **A**: `ProbePoolSize=8` fixed; `ProbeRateMultiplier ∈ {4, 2√2, 2, √2, 1, 1/√2,
  0.5}`; 30s measurement window, no warmup/drain.
- **B**: `ProbeRateMultiplier=3.0` fixed; `ProbePoolSize ∈ {1, 2, 4, 8, 16, 24, 32}`;
  30s measurement, 15s warmup, 5s drain.

**Deviations from the Original Setup:**

- **Scale.** The paper evaluates production fleets with hundreds of replicas; our
  local hardware (12 cores) limits us to 10/50/100-backend fleets. Since
  `CPUsPerBackend=1.0` is fixed regardless of fleet size, 10/50/100 backends
  correspond to 0.83x/4.2x/8.3x CPU oversubscription on the host — an additional,
  unintended source of contention on top of the antagonist model, most severe at
  100 backends (see Sections 4.3 and 5).
- **Round Robin probes in our implementation.** In the paper, RR is a pure
  client-local baseline that never probes. In our implementation, RR shares the same
  background probing infrastructure as Prequal (`fireProbes()` /`probeOne()` run
  unconditionally for both algorithms) — RR uses only the resulting health-check
  signal (`IsHealthy`), not the RIF pool. We kept this because it is a controlled-
  comparison design choice (both algorithms see identical background probe traffic
  and server health state), and because RR's error-rate sensitivity to probe rate
  turned out to be a useful diagnostic (Section 4.5).
- **Load model.** The paper does not specify the exact workload/contention
  generator used for Figure 8. We designed our own 5-state antagonist random walk
  (above), calibrated so the system's steady state matches the paper's stated
  "~1.5x allocation, worst case" only during occasional ceiling excursions, rather
  than as a permanent condition (an earlier, harsher calibration made *every* step
  collapse to 40-68% errors — see comments in `scripts/run_experiment_a.sh`).
- **Probe pool eviction.** The paper's pool periodically evicts its *worst* probe
  (by RIF); our implementation evicts the *oldest* probe on overflow, a
  simplification (`pkg/loadbalancer/balancer.go`, `updatePool`).

# 4. Experiment Result

## 4.1 Execution and Measurement

`cmd/experiment` orchestrates each run: for every (algorithm, sweep-step)
combination it regenerates `docker-compose.yml` via `compose-gen` with that step's
`ProbePoolSize`/`ProbeRateMultiplier`, brings up an isolated stack (one LB + N
backends + Prometheus/Grafana), runs the containerized load generator
(`docker compose --profile tools run loadgen`) for the configured
duration(+warmup+drain), and records: end-to-end p50/p99/p99.9 latency and error
count from the load generator, plus the selected replica's RIF p50/p75/p99 scraped
from the LB's Prometheus metrics. Each row of the result CSVs is **one 30-second run**
— we did not run repeated trials or compute confidence intervals, which we note as a
limitation in Section 6.

To check correctness we verified `total_requests` against the expected `QPS ×
duration`, confirmed RIF percentiles moved in the expected direction with probe pool
size/rate, and used the Grafana dashboards during development to confirm backend
CPU/queue behavior matched the configured antagonist model.

## 4.2 Debugging Notes

- **Host-port relay artifact.** Early runs pointed the load generator at
  `localhost:<published-port>`. On Docker Desktop/WSL2 this traffic crosses a slow
  host↔VM relay that itself becomes the bottleneck, producing ~100% error rates that
  reflected the relay, not the LB. Fix: always run the load generator as a container
  on the compose network (`-url http://lb-prequal:8080`); we also added a runtime
  warning for loopback URLs (`cmd/loadgen/main.go`, `warnIfLoopback`).
- **Probe-worker pool sizing.** At 50-backend/2500-QPS scale, the original
  `probeWorkers`/`probeQueueSize = 50/100` (sized for the 10-backend/500-QPS
  baseline) was too small: probes were dropped under load, the pool went stale, and
  both algorithms occasionally routed into already-overloaded backends (0.4-3.5%
  errors, `results_b_v1.csv`). Scaling 3x to `150/300` eliminated these errors with
  unchanged latency trends (`results_b_v2.csv`). We also tried `400/800` for the
  100-backend runs; this *increased* total errors (8181-29576 vs 2615 at 150/300),
  so `150/300` was kept as the validated setting at all scales.
- **Docker build concurrency.** Building all 100 backend images in parallel
  occasionally failed with buildkit `"no such job"` errors; resolved by capping
  build concurrency with `COMPOSE_PARALLEL_LIMIT=8`.

## 4.3 Experiment A — Probe-Rate Sweep (Figure 8 replica)

![10-backend Experiment A latency](plots/10backend/expA_latency.png)
![10-backend Experiment A RIF](plots/10backend/expA_rif.png)

**10 backends** (`results_a_v30.csv`): at this scale, each backend represents 10% of
total fleet capacity, so a single backend's excursion into the antagonist's 80%-load
state has an outsized effect on a policy that ignores it. Prequal's p99 stays flat at
**300-660ms** across the entire probe-rate sweep (0.5x-4x), while RR's p99 swings
between **2.6s and 4.8s** — a **5-10x** gap — because RR keeps routing its fixed 1/10
share of traffic to whichever backend happens to be saturated, while Prequal's probes
let it route around it. RR also accumulates 561 errors total across the sweep
(Prequal: 0).

![50-backend Experiment A latency](plots/50backend/expA_latency.png)
![50-backend Experiment A RIF](plots/50backend/expA_rif.png)

**50 backends** (`results_a_50.csv` — our cleanest run, see below): Prequal's p99
advantage narrows to a consistent **~25-35%** (439-657ms vs 688-840ms for RR) across
the whole probe-rate range — each backend now represents only 2% of capacity, so
RR's "blind spot" cost shrinks, but Prequal still wins consistently. Errors appear
**only at probe rate ≤ 1.0x**: Prequal 361 @ 1.0x and 384 @ 0.5x; RR 220 @ 0.71x and
392 @ 0.5x (total 1357) — matching the paper's claim that effects become significant
once probing drops below one probe per query (mechanism discussed in 4.5).

![100-backend Experiment A latency](plots/100backend/expA_latency.png)
![100-backend Experiment A RIF](plots/100backend/expA_rif.png)

**100 backends** (`results_a_100.csv` / `results_a_100_v2.csv`): the same
qualitative pattern holds (Prequal generally below RR on p99; errors increase below
1x probe rate), but total errors balloon to 5531 / 7882 across two independent
re-runs — far noisier than at 50 backends. We attribute this to the
CPU-oversubscription confound described in Section 3 (8.3x at 100 backends), which
adds host-level contention on top of the antagonist model's designed contention.

**Headline result for A**: the **50-backend run** (`results_a_50.csv`) — a clean,
consistent ~25-35% Prequal p99 advantage over RR across the full probe-rate sweep,
with both algorithms' error rates rising only below 1x probe rate, consistent with
the paper's Figure 8.

## 4.4 Experiment B — Probe Pool Size Sweep

![50-backend Experiment B latency](plots/50backend/expB_latency.png)
![50-backend Experiment B RIF](plots/50backend/expB_rif.png)

**50 backends** (`results_b_v2.csv`, 0 errors across all 14 steps): Prequal's p99
drops sharply from **1020ms** (P=1) to **~460ms** by P=8, then is essentially **flat**
(460→456→455→449ms) through P=32. RR stays flat at ~700-790ms throughout (it
doesn't use the pool). Prequal starts *worse* than RR at P≤2 (a too-small/stale pool
hurts more than not having one), crosses over by P=4, and settles ~40% better than RR
from P=8 onward.

![100-backend Experiment B latency](plots/100backend/expB_latency.png)
![100-backend Experiment B RIF](plots/100backend/expB_rif.png)

**100 backends** (`results_b_100.csv`): Prequal's p99 decreases **monotonically**
across the whole sweep, **1898ms** (P=1) → **567ms** (P=32), with **no flattening**.
RR again stays flat at ~720-805ms. Prequal crosses below RR around P=8 and reaches
~25% better than RR by P=32. Errors are modest (Prequal=308, RR=2307 total).

**Headline result for B**: the **100-backend run**, because — as discussed in
Section 5 — at 50 backends a pool of size ≥8 already represents ≥16% of the entire
fleet, large enough that the curve saturates; the 100-backend run keeps the pool
below ~32% of the fleet throughout the sweep and shows the pool-size effect the
paper's design is meant to address.

## 4.5 Why does Prequal show nonzero errors?

In Experiment A, Prequal's errors appear *only* at probe rate ≤ 1.0x query rate (e.g.
50 backends: 361 @ 1.0x, 384 @ 0.5x). Each backend's antagonist load follows a random
walk with ~2s mean dwell time; below ~1 probe per query, the probe pool is refreshed
too infrequently to track these swings. Both algorithms can then route a request to a
backend whose pool entry says "lightly loaded" but which has since transitioned to
its 80%-antagonist (overloaded) state; the backend's bounded queue
(`queueDepth = saturationQPS * 20`) overflows and it returns an error. This reproduces
— as a hard error rather than only an elevated tail-latency/RIF signature — the exact
threshold the paper describes: "at probing rates of 1/√2x and 1/2x, the tail RIF
distributions jump visibly."

# 5. Further Exploration

## 5.1 Research Question

The paper's design rationale (Section 5) explicitly assumes "each client's probe pool
represents only a small random subset of replicas" — its default pool size of 16 is
chosen against fleets of hundreds-plus replicas. Our Experiment B sweeps the pool
size up to 32, but at only 50 backends, P=32 is **64%** of the fleet — no longer "a
small subset". We hypothesized this explains why the 50-backend curve (Section 4.4)
flattens by P=8 (16% of fleet) while the 100-backend curve (P=32 = 32% of fleet) keeps
improving across the whole sweep: **the relevant variable is the pool-to-fleet
ratio, not the absolute pool size.**

## 5.2 Methodology and Result

To test this directly, we extended Experiment B's pool-size sweep to
`{1, 2, 4, 8, 16, 24, 32, 40, 48}` at 100 backends (`results_b_100_v4.csv`), aiming
to find the "elbow" where the 100-backend curve starts to flatten too (P=48 = 48% of
fleet).

**Result**: this run was too noisy to use. Total errors jumped to **22,286** (vs
2,615 for the same P=1-32 range measured earlier in `results_b_100.csv`), including
for Round Robin — which doesn't use the pool for selection at all, and whose error
rate should therefore be independent of P. Since even RR's error rate roughly
tripled, this points to a confound external to the pool-size variable: by this point
in the session we had run many back-to-back 100-backend experiments, accumulating
~70GB of Docker images/build cache and host-level resource pressure on top of the
already-severe 8.3x CPU oversubscription at 100 backends. We therefore cannot draw a
conclusion about a 40/48 elbow from this run.

**What the clean data does show**, comparing the 50- and 100-backend curves at the
*same absolute pool sizes* (P=1-32):

| Fleet size | P as % of fleet at P=32 | Prequal p99 trend P=8→32 |
|---|---|---|
| 50 backends  | 64% | flat (460→449ms) |
| 100 backends | 32% | still decreasing (721→567ms) |

This is consistent with our hypothesis: a pool-size sweep that reaches 64% of the
fleet tests a fundamentally different (near-full-visibility) regime than the paper's
intended "thin random sample", while the same absolute sweep against a larger fleet
(32% of fleet) stays closer to that regime and shows a richer, still-improving curve.
A rigorous test of where the curve *does* eventually flatten for a 100-backend fleet
would require either an even larger fleet (200+ backends, infeasible on our
hardware) or a freshly-cleaned host environment with multiple repetitions per data
point to separate measurement noise from the real effect — both left as future work.

# 6. Reproducibility Assessment of the Paper

- **Methodology**: the paper describes the HCL selection rule, probe-pool
  maintenance algorithm, and the probing-rate/pool-size trade-offs in good
  conceptual detail (Sections 5-6), and Figure 8's headline claim ("insensitive to
  probe rate above 1x, degrades below") is reproducible at a qualitative level even
  at 1-2 orders of magnitude smaller scale (10-100 backends vs. the paper's
  production fleets).
- **Artifact**: no public code artifact was available. We reimplemented the load
  balancer, HCL selection rule, probe pool, and a synthetic antagonist-load backend
  from scratch in Go, based solely on the paper's prose description.
- **Missing details**: the paper does not specify the exact workload-generation or
  contention-injection process used for Figure 8. We designed and calibrated our own
  5-state antagonist random walk to reproduce the paper's stated "~1.5x allocation,
  worst case" condition as an occasional excursion rather than a permanent state —
  an assumption we made explicit in Section 3.
- **Difficulty**: moderate-to-high. The core algorithm (HCL rule, RIF + latency
  scoring, bounded probe pool) was directly implementable from the paper's
  description. The harder part was constructing a load model whose *dynamics*
  (not just average load) create the heterogeneity Prequal is designed to exploit —
  this took several calibration iterations (see `scripts/run_experiment_a.sh`
  comments on an earlier bias configuration that caused 40-68% error rates at every
  sweep step). Reproducing the paper's exact numbers was not attempted, consistent
  with the assignment's framing — only qualitative trends.

# 7. Conclusion

- **Experiment A** (Figure 8 replica) qualitatively reproduces the paper's central
  claim: Prequal (HCL + probing) beats Round Robin on tail latency, with the gap
  *widening as fleet size shrinks* (10 backends: RR p99 5-10x worse; 50 backends: RR
  p99 ~25-35% worse), and both algorithms' error rates rise once the probe rate drops
  below ~1x the query rate — exactly the threshold the paper identifies.
- **Experiment B** shows Prequal's p99 improves with probe pool size, with
  diminishing returns once the pool covers a large fraction of the fleet: at 50
  backends (P≤32 = ≤64% of fleet) the curve flattens by P=8, while at 100 backends
  (P≤32 = ≤32% of fleet) it keeps improving throughout the sweep — supporting the
  paper's framing of the probe pool as "a small random subset of replicas."
- Our further-exploration attempt to push this ratio further (P up to 48 at 100
  backends) was inconclusive due to host resource-contention noise accumulated over a
  long session of 100-backend runs — a practical lesson that, at this scale, each run
  needs a freshly-cleaned Docker environment and (ideally) multiple repetitions per
  data point, a limitation of our single-run-per-point methodology throughout.
- Overall, despite operating at a much smaller scale than the paper's production
  deployment, our reproduction supports both of Prequal's central qualitative claims:
  (1) probe-based RIF/latency-aware selection meaningfully beats Round Robin on tail
  latency, especially for smaller fleets where a single overloaded replica matters
  more; and (2) the system's sensitivity to probing rate and pool size matches the
  thresholds the paper describes (~1x probe rate; pool size as a small fraction of
  the fleet).
