#!/usr/bin/env bash
set -euo pipefail

# --- config ---
TARGETS="${TARGETS:-targets.txt}"
RATE="${RATE:-5000}"
DURATION="${DURATION:-60s}"
PPROF_URL="${PPROF_URL:-http://localhost:6060/debug/pprof/profile}"
PPROF_SECONDS="${PPROF_SECONDS:-60}"
OUTDIR="${OUTDIR:-./bench-$(date +%Y%m%d-%H%M%S)}"

mkdir -p "$OUTDIR"

VEGETA_OUT="$OUTDIR/vegeta.bin"
VEGETA_TXT="$OUTDIR/vegeta.txt"
PPROF_OUT="$OUTDIR/pprof.pb.gz"
PPROF_TXT="$OUTDIR/pprof.txt"

echo "==> output dir: $OUTDIR"
echo "==> targets:    $TARGETS"
echo "==> rate:       $RATE for $DURATION"
echo "==> pprof:      $PPROF_URL (${PPROF_SECONDS}s)"
echo

# --- sanity checks ---
[[ -f "$TARGETS" ]] || { echo "targets file not found: $TARGETS" >&2; exit 1; }
command -v vegeta >/dev/null || { echo "vegeta not in PATH" >&2; exit 1; }
command -v go     >/dev/null || { echo "go not in PATH" >&2; exit 1; }

# --- run vegeta in background ---
echo "==> starting vegeta..."
vegeta attack \
    -targets="$TARGETS" \
    -rate="$RATE" \
    -duration="$DURATION" \
    > "$VEGETA_OUT" &
VEGETA_PID=$!

# small delay so the load is actually flowing before we start profiling
sleep 2

# --- fetch pprof while vegeta is running ---
echo "==> fetching pprof for ${PPROF_SECONDS}s..."
if ! go tool pprof -proto -output="$PPROF_OUT" "${PPROF_URL}?seconds=${PPROF_SECONDS}" 2>/dev/null; then
    # fallback: plain curl if pprof URL isn't reachable
    echo "==> pprof via go tool failed, trying curl..."
    curl -sS -o "$PPROF_OUT" "${PPROF_URL}?seconds=${PPROF_SECONDS}" || {
        echo "!! failed to fetch profile" >&2
    }
fi

# --- wait for vegeta to finish ---
echo "==> waiting for vegeta (pid $VEGETA_PID)..."
wait "$VEGETA_PID" || true

# --- reports ---
echo "==> generating vegeta report..."
{
    echo "### attack: rate=$RATE duration=$DURATION targets=$TARGETS"
    echo
    echo "### summary"
    vegeta report "$VEGETA_OUT"
    echo
    echo "### histogram"
    vegeta report -type='hist[0,1ms,5ms,10ms,50ms,100ms,500ms,1s,5s]' "$VEGETA_OUT"
    echo
    echo "### json"
    vegeta report -type=json "$VEGETA_OUT"
} | tee "$VEGETA_TXT"

if [[ -s "$PPROF_OUT" ]]; then
    echo
    echo "==> pprof top (flat) -> $PPROF_TXT"
    {
        echo "### pprof top (flat)"
        go tool pprof -top -nodecount=40 "$PPROF_OUT"
        echo
        echo "### pprof top (cum)"
        go tool pprof -top -cum -nodecount=40 "$PPROF_OUT"
    } | tee "$PPROF_TXT"
else
    echo "!! no pprof data collected" >&2
fi

echo
echo "==> done."
echo "    vegeta raw:  $VEGETA_OUT"
echo "    vegeta txt:  $VEGETA_TXT"
echo "    pprof raw:   $PPROF_OUT"
echo "    pprof txt:   $PPROF_TXT"
echo
echo "    interactive: go tool pprof -http=:8081 $PPROF_OUT"
