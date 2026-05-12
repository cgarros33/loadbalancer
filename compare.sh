#!/bin/bash

set -e

print_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Run side-by-side comparison test of Prequal vs Round-Robin.

OPTIONS:
    -t, --type TYPE         Benchmark type: 'load' or 'probe' (default: load)
    -d, --duration SEC      Duration per level/step (default: 120)
    -h, --help              Show this help message

BENCHMARK TYPES:
    load    Ramps load from 75% to 174% of baseline capacity in multiplicative
            steps of 10/9, matching the methodology from Figure 6 in the paper.
            Both algorithms are tested simultaneously at each level.

    probe   Varies the probing rate from 4× to ½× the query rate in 6
            multiplicative steps of √2, matching the methodology from
            Figure 8 in the paper. The system is run "very hot" at 1.5×
            of baseline capacity. Tests probe rates:
              4×, 2.83×, 2×, 1.41×, 1×, 0.71×, 0.50×
            Round-Robin serves as a probe-independent baseline.

REQUIREMENTS:
    - hey must be installed: go install github.com/rakyll/hey@latest
    - jq must be installed (for probe benchmark): sudo apt install jq
    - Both load balancers must be running (docker-compose up)

EXAMPLES:
    ./compare.sh --type load --duration 120
    ./compare.sh --type probe --duration 60

EOF
}

check_hey() {
    if ! command -v hey &> /dev/null; then
        echo "Error: hey is not installed"
        echo "Install with: go install github.com/rakyll/hey@latest"
        exit 1
    fi
}

check_jq() {
    if ! command -v jq &> /dev/null; then
        echo "Error: jq is not installed"
        echo "Install with: sudo apt install jq"
        exit 1
    fi
}

check_services() {
    echo "Checking services..."
    if ! curl -s http://localhost:8080/health > /dev/null 2>&1; then
        echo "Error: Prequal load balancer not responding on port 8080"
        echo "Start services with: docker-compose up -d"
        exit 1
    fi
    if ! curl -s http://localhost:8081/health > /dev/null 2>&1; then
        echo "Error: Round-Robin load balancer not responding on port 8081"
        echo "Start services with: docker-compose up -d"
        exit 1
    fi
    echo "Both load balancers are running"
}

calibrate_baseline() {
    echo "Determining baseline capacity..."
    echo "Running calibration test (30s on Prequal)..."
    BASELINE=$(hey -z 30s -q 100 http://localhost:8080 2>&1 | grep "Requests/sec:" | awk '{print $2}')
    echo "Baseline capacity: ${BASELINE} req/sec"
    echo ""
}

# ── Load Benchmark ───────────────────────────────────────────────────────────

run_load_benchmark() {
    echo ""
    echo "========================================="
    echo "  Load Benchmark"
    echo "  Prequal (8080) vs Round-Robin (8081)"
    echo "========================================="
    echo "Duration per level: ${DURATION}s"
    echo ""

    calibrate_baseline

    LEVELS=(0.75 0.83 0.93 1.03 1.14 1.27 1.41 1.57 1.74)
    LEVEL_NAMES=("75%" "83%" "93%" "103%" "114%" "127%" "141%" "157%" "174%")

    for i in "${!LEVELS[@]}"; do
        level=${LEVELS[$i]}
        name=${LEVEL_NAMES[$i]}
        qps=$(echo "$BASELINE * $level" | bc -l | awk '{printf "%.0f", $1}')

        echo "========================================="
        echo "Step $((i+1))/9: Load Level $name"
        echo "Target: ${qps} req/sec per algorithm"
        echo "========================================="
        echo ""

        echo "Starting load test on both algorithms..."

        hey -z ${DURATION}s -q $qps http://localhost:8080 > /tmp/prequal_${i}.txt 2>&1 &
        PID_PREQUAL=$!

        hey -z ${DURATION}s -q $qps http://localhost:8081 > /tmp/rr_${i}.txt 2>&1 &
        PID_RR=$!

        wait $PID_PREQUAL
        wait $PID_RR

        echo ""
        echo "--- Prequal Results ---"
        grep -E "Requests/sec:|p50|p99|p99.9" /tmp/prequal_${i}.txt | head -5

        echo ""
        echo "--- Round-Robin Results ---"
        grep -E "Requests/sec:|p50|p99|p99.9" /tmp/rr_${i}.txt | head -5

        echo ""
        echo "Completed step $((i+1))/9"
        echo ""

        if [ $i -lt 8 ]; then
            echo "Pausing 10 seconds before next level..."
            sleep 10
        fi
    done
}

# ── Probe Rate Benchmark (Paper Figure 8) ─────────────────────────────────────
#
# Methodology from the paper:
#   "We ramp down the probing rate from 4x to ½x the query rate,
#    in 6 multiplicative steps of √2 each, while keeping the probe
#    removal rate steady at 0.25 per query. [...] we ran the system
#    very hot, at roughly 1.5x our CPU allocation throughout."
#
# Probe rates: 4, 4/√2≈2.83, 2, √2≈1.41, 1, 1/√2≈0.71, 1/2=0.50
#
# Mapping to our implementation:
#   - For rates ≥ 1: selection_choices = round(rate)
#   - For rates < 1: selection_choices = 1 and probe_interval_ms is
#     increased to simulate staler/less frequent probe data.

run_probe_benchmark() {
    check_jq

    echo ""
    echo "========================================="
    echo "  Probe Rate Benchmark (Paper Fig. 8)"
    echo "  Probing rate: 4× → ½× in √2 steps"
    echo "  Load: 1.5× baseline (hot)"
    echo "  Round-Robin as constant baseline"
    echo "========================================="
    echo "Duration per probe rate: ${DURATION}s"
    echo ""

    calibrate_baseline

    # Fixed load at 1.5× baseline ("very hot") — as specified in the paper
    QPS=$(echo "$BASELINE * 1.5" | bc -l | awk '{printf "%.0f", $1}')
    echo "Fixed load: ${QPS} req/sec (1.5× baseline)"
    echo ""

    # Probe rate multipliers: 4× down to ½× in 6 steps of √2
    #   4, 4/√2, 4/2, 4/(2√2), 4/4, 4/(4√2), 4/8
    PROBE_RATE_LABELS=("4.00x" "2.83x" "2.00x" "1.41x" "1.00x" "0.71x" "0.50x")
    PROBE_RATE_VALUES=(4.00   2.83    2.00    1.41    1.00    0.71    0.50)

    # Map to selection_choices (integer, min 1) and probe_interval_ms
    # Default probe_interval_ms from config (1000ms)
    BASE_PROBE_INTERVAL=1000
    SELECTION_CHOICES=()
    PROBE_INTERVALS=()
    for rate in "${PROBE_RATE_VALUES[@]}"; do
        # selection_choices = max(1, round(rate))
        choices=$(echo "$rate" | awk '{v=int($1+0.5); if(v<1) v=1; print v}')
        SELECTION_CHOICES+=("$choices")

        # For sub-1 rates, increase probe interval to simulate staler data
        # interval = base_interval / min(rate, 1)
        interval=$(echo "$BASE_PROBE_INTERVAL $rate" | awk '{r=$2; if(r<1) r=$2; else r=1; printf "%.0f", $1/r}')
        PROBE_INTERVALS+=("$interval")
    done

    TOTAL=${#PROBE_RATE_VALUES[@]}

    # Save original config
    cp loadbalancer.json loadbalancer.json.bak
    trap 'echo ""; echo "Restoring original config..."; cp loadbalancer.json.bak loadbalancer.json; rm -f loadbalancer.json.bak' EXIT

    # Run Round-Robin baseline once (probe-independent)
    echo "========================================="
    echo "Baseline: Round-Robin (probe-independent)"
    echo "========================================="
    echo ""
    echo "Running Round-Robin at ${QPS} req/sec for ${DURATION}s..."

    hey -z ${DURATION}s -q $QPS http://localhost:8081 > /tmp/probe_rr_baseline.txt 2>&1

    echo ""
    echo "--- Round-Robin Baseline Results ---"
    grep -E "Requests/sec:|50%|90%|99%|99\.9%" /tmp/probe_rr_baseline.txt | head -5
    echo ""

    echo "Pausing 10 seconds before probe sweep..."
    sleep 10

    # Sweep probe rates from 4× down to ½×
    for i in "${!PROBE_RATE_VALUES[@]}"; do
        label=${PROBE_RATE_LABELS[$i]}
        rate=${PROBE_RATE_VALUES[$i]}
        choices=${SELECTION_CHOICES[$i]}
        interval=${PROBE_INTERVALS[$i]}

        echo "========================================="
        echo "Step $((i+1))/${TOTAL}: Probe Rate = ${label}"
        echo "  selection_choices = ${choices}"
        echo "  probe_interval_ms = ${interval}"
        echo "========================================="
        echo ""

        # Update config: selection_choices and probe_interval_ms
        jq ".prequal.selection_choices = ${choices} | .prequal.probe_interval_ms = ${interval}" \
            loadbalancer.json.bak > loadbalancer.json

        # Restart the Prequal LB to pick up the new config
        echo "Restarting Prequal LB..."
        docker compose restart loadbalancer-prequal > /dev/null 2>&1

        # Wait for it to be healthy
        echo "Waiting for Prequal LB to be ready..."
        for attempt in $(seq 1 30); do
            if curl -s http://localhost:8080/health > /dev/null 2>&1; then
                break
            fi
            sleep 1
        done

        if ! curl -s http://localhost:8080/health > /dev/null 2>&1; then
            echo "Error: Prequal LB failed to restart"
            exit 1
        fi
        echo "Prequal LB ready"

        # Let probes stabilize
        echo "Waiting 5 seconds for probe data to stabilize..."
        sleep 5

        # Run load test against Prequal
        echo "Running Prequal at ${QPS} req/sec for ${DURATION}s..."
        # Use a safe filename tag (replace dot with underscore)
        tag=$(echo "$rate" | tr '.' '_')
        hey -z ${DURATION}s -q $QPS http://localhost:8080 > /tmp/probe_prequal_${tag}.txt 2>&1

        echo ""
        echo "--- Prequal (probe rate = ${label}) ---"
        grep -E "Requests/sec:|50%|90%|99%|99\.9%" /tmp/probe_prequal_${tag}.txt | head -5

        echo ""
        echo "Completed step $((i+1))/${TOTAL}"
        echo ""

        if [ $i -lt $((TOTAL - 1)) ]; then
            echo "Pausing 10 seconds before next probe rate..."
            sleep 10
        fi
    done

    # Summary table
    echo ""
    echo "========================================="
    echo "  Probe Rate Summary (load = 1.5× baseline)"
    echo "========================================="
    echo ""
    printf "%-15s %10s %10s %10s %10s\n" "Probe Rate" "p50" "p90" "p99" "p99.9"
    printf "%-15s %10s %10s %10s %10s\n" "-------------" "--------" "--------" "--------" "--------"

    # RR baseline
    p50=$(grep "50%" /tmp/probe_rr_baseline.txt 2>/dev/null | awk '{print $2}' | head -1)
    p90=$(grep "90%" /tmp/probe_rr_baseline.txt 2>/dev/null | awk '{print $2}' | head -1)
    p99=$(grep "99%" /tmp/probe_rr_baseline.txt 2>/dev/null | awk '{print $2}' | head -1)
    p999=$(grep "99.9%" /tmp/probe_rr_baseline.txt 2>/dev/null | awk '{print $2}' | head -1)
    printf "%-15s %10s %10s %10s %10s\n" "RR (baseline)" "${p50:-n/a}" "${p90:-n/a}" "${p99:-n/a}" "${p999:-n/a}"

    # Prequal at each probe rate
    for i in "${!PROBE_RATE_VALUES[@]}"; do
        label=${PROBE_RATE_LABELS[$i]}
        rate=${PROBE_RATE_VALUES[$i]}
        tag=$(echo "$rate" | tr '.' '_')
        p50=$(grep "50%" /tmp/probe_prequal_${tag}.txt 2>/dev/null | awk '{print $2}' | head -1)
        p90=$(grep "90%" /tmp/probe_prequal_${tag}.txt 2>/dev/null | awk '{print $2}' | head -1)
        p99=$(grep "99%" /tmp/probe_prequal_${tag}.txt 2>/dev/null | awk '{print $2}' | head -1)
        p999=$(grep "99.9%" /tmp/probe_prequal_${tag}.txt 2>/dev/null | awk '{print $2}' | head -1)
        printf "%-15s %10s %10s %10s %10s\n" "PQ ${label}" "${p50:-n/a}" "${p90:-n/a}" "${p99:-n/a}" "${p999:-n/a}"
    done
}

# ── Main ──────────────────────────────────────────────────────────────────────

DURATION=120
BENCHMARK_TYPE="load"

while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--type)
            BENCHMARK_TYPE="$2"
            shift 2
            ;;
        -d|--duration)
            DURATION="$2"
            shift 2
            ;;
        -h|--help)
            print_usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            print_usage
            exit 1
            ;;
    esac
done

check_hey
check_services

case $BENCHMARK_TYPE in
    load)
        run_load_benchmark
        ;;
    probe)
        run_probe_benchmark
        ;;
    *)
        echo "Error: Unknown benchmark type '${BENCHMARK_TYPE}'"
        echo "Valid types: load, probe"
        exit 1
        ;;
esac

echo ""
echo "========================================="
echo "         Test Complete"
echo "========================================="
echo ""
echo "View comparison in Grafana:"
echo "  http://localhost:3001"
echo ""
echo "Use the algorithm dropdown to filter or show both"
echo ""
echo "Detailed results saved in /tmp/prequal_*.txt and /tmp/rr_*.txt"
