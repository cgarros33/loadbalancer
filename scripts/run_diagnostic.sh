#!/usr/bin/env bash
# Diagnostic: observe actual routing and service times at low and high QPS.
#
# Qué muestra:
#   - Qué fracción de requests llegó a hot vs cold backends (effective_ms en los logs)
#   - Distribution de arrival_rif por backend
#   - Comportamiento de Prequal vs RR
#
# Usage: ./scripts/run_diagnostic.sh

set -euo pipefail

COMPOSE_FILE=docker-compose-diag.yml
LOADGEN=./bin/loadgen
COMPOSEGEN=./bin/compose-gen

for bin in "$LOADGEN" "$COMPOSEGEN"; do
    if [[ ! -x "$bin" ]]; then
        echo "binary not found: $bin — run: make build" >&2
        exit 1
    fi
done

cleanup() {
    echo ""
    echo "=== tearing down ==="
    docker compose -f "$COMPOSE_FILE" down --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

# ── generate compose with DEBUG=1 ──────────────────────────────────────────
echo "=== generating compose (DEBUG=1, queueDepth=20x) ==="
"$COMPOSEGEN" \
    -n               10   \
    -hot-fraction    0.3  \
    -hot-cpu-load    80   \
    -cpu-load        10   \
    -base-service-ms 50   \
    -capacity        10   \
    -qrif            0.70 \
    -probe-pool-size 8    \
    -probe-rate-multiplier 4.0 \
    -debug           1    \
    -output          "$COMPOSE_FILE"

# ── start containers ────────────────────────────────────────────────────────
echo "=== docker compose up ==="
docker compose -f "$COMPOSE_FILE" up -d --build --pull missing
echo "waiting 20s for backends to settle..."
sleep 20

# ── helper: run load + analyze DEBUG logs ───────────────────────────────────
run_phase() {
    local label="$1"
    local url="$2"
    local qps="$3"
    local dur="$4"

    echo ""
    echo "════════════════════════════════════════════"
    echo "  $label  (${qps} QPS × ${dur}s)"
    echo "════════════════════════════════════════════"

    "$LOADGEN" --url "$url" --qps "$qps" --duration "${dur}s"

    echo ""
    echo "--- routing: hot vs cold backends ---"
    # DEBUG=1 logs: "path=/ arrival_rif=N capacity=10 effective_ms=250.00 sample_ms=247.33"
    # hot backends have effective_ms=250 (antagonist=80%, base=50, cap=10 → 50*10/2=250)
    # cold backends have effective_ms=56  (antagonist=10%, base=50, cap=10 → 50*10/9=55.6)
    docker compose -f "$COMPOSE_FILE" logs --no-color --since "${dur}s" 2>/dev/null \
        | grep 'effective_ms=' \
        | awk '
            {
                for (i=1;i<=NF;i++) {
                    if ($i ~ /^effective_ms=/) {
                        split($i, a, "=")
                        ms = a[2]+0
                        if (ms > 100) hot++
                        else          cold++
                        total++
                    }
                }
            }
            END {
                printf "  total logged requests : %d\n", total
                if (total > 0) {
                    printf "  → cold backend (ms≤100): %d  (%.1f%%)\n", cold, cold*100/total
                    printf "  → hot  backend (ms>100): %d  (%.1f%%)\n", hot,  hot*100/total
                }
            }'

    echo ""
    echo "--- arrival RIF distribution ---"
    docker compose -f "$COMPOSE_FILE" logs --no-color --since "${dur}s" 2>/dev/null \
        | grep 'arrival_rif=' \
        | grep -o 'arrival_rif=[0-9]*' \
        | awk -F= '{
            rif = $2+0
            if      (rif == 0)  b0++
            else if (rif <= 5)  b5++
            else if (rif <= 15) b15++
            else if (rif <= 50) b50++
            else                bhigh++
            total++
        }
        END {
            printf "  rif=0        : %d\n", b0
            printf "  rif=1-5      : %d\n", b5
            printf "  rif=6-15     : %d\n", b15
            printf "  rif=16-50    : %d\n", b50
            printf "  rif>50       : %d\n", bhigh
        }'
}

# ── Phase 1: Prequal at 50 QPS (well within cold capacity) ──────────────────
run_phase "PREQUAL  50 QPS" "http://localhost:8080/" 50 30

# ── Phase 2: RR at 50 QPS ────────────────────────────────────────────────────
run_phase "RR       50 QPS" "http://localhost:8081/" 50 30

# ── Phase 3: Prequal at 500 QPS (stress case) ────────────────────────────────
run_phase "PREQUAL 500 QPS" "http://localhost:8080/" 500 30

# ── Phase 4: RR at 500 QPS ───────────────────────────────────────────────────
run_phase "RR      500 QPS" "http://localhost:8081/" 500 30

echo ""
echo "=== diagnostic complete ==="
echo "Full logs si los necesitás: docker compose -f $COMPOSE_FILE logs"
