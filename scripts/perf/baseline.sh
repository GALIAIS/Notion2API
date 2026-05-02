#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PAYLOAD_TEMPLATE="${ROOT_DIR}/scripts/perf/payload-chat.json"
OUT_ROOT="${ROOT_DIR}/docs/perf"

BASE_URL="${N2A_BASE_URL:-http://127.0.0.1:8787}"
PPROF_BASE="${N2A_PPROF_BASE:-http://127.0.0.1:6060}"
API_KEY="${N2A_API_KEY:-change-me-openai-key}"
CONCURRENCY="${N2A_PERF_CONCURRENCY:-50}"
DURATION="${N2A_PERF_DURATION:-60s}"

if [[ ! -f "${PAYLOAD_TEMPLATE}" ]]; then
  echo "payload template not found: ${PAYLOAD_TEMPLATE}" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

if ! command -v python >/dev/null 2>&1; then
  echo "python is required" >&2
  exit 1
fi

LOAD_TOOL=""
if command -v hey >/dev/null 2>&1; then
  LOAD_TOOL="hey"
elif command -v vegeta >/dev/null 2>&1; then
  LOAD_TOOL="vegeta"
else
  echo "either hey or vegeta must be installed" >&2
  exit 1
fi

if ! curl -fsS "${BASE_URL}/healthz" >/dev/null; then
  echo "service is not reachable: ${BASE_URL}/healthz" >&2
  exit 1
fi

if ! curl -fsS "${PPROF_BASE}/debug/pprof/" >/dev/null; then
  echo "pprof endpoint is not reachable: ${PPROF_BASE}/debug/pprof/" >&2
  echo "enable config.debug.pprof_enabled=true and keep pprof_addr local-only." >&2
  exit 1
fi

stamp="$(date -u +%Y%m%d-%H%M%S)"
git_sha="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "nogit")"
OUT_DIR="${OUT_ROOT}/${stamp}-${git_sha}"
mkdir -p "${OUT_DIR}"

tmp_dir="$(mktemp -d "${OUT_DIR}/tmp.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

non_stream_payload="${tmp_dir}/chat.nonstream.json"
stream_payload="${tmp_dir}/chat.stream.json"
python - "${PAYLOAD_TEMPLATE}" "${non_stream_payload}" "${stream_payload}" <<'PY'
import json
import sys
from pathlib import Path

template = Path(sys.argv[1])
non_stream = Path(sys.argv[2])
stream = Path(sys.argv[3])
payload = json.loads(template.read_text(encoding="utf-8"))

payload_non_stream = dict(payload)
payload_non_stream["stream"] = False
non_stream.write_text(json.dumps(payload_non_stream, ensure_ascii=False), encoding="utf-8")

payload_stream = dict(payload)
payload_stream["stream"] = True
stream.write_text(json.dumps(payload_stream, ensure_ascii=False), encoding="utf-8")
PY

request_url="${BASE_URL}/v1/chat/completions"
auth_header="Authorization: Bearer ${API_KEY}"
content_header="Content-Type: application/json"

run_hey() {
  local payload_file="$1"
  local output_file="$2"
  hey -z "${DURATION}" -c "${CONCURRENCY}" -m POST \
    -H "${auth_header}" \
    -H "${content_header}" \
    -D "${payload_file}" \
    "${request_url}" >"${output_file}"
}

run_vegeta() {
  local payload_file="$1"
  local output_txt="$2"
  local output_bin="$3"
  local output_json="$4"
  printf "POST %s\n" "${request_url}" | vegeta attack \
    -duration="${DURATION}" \
    -workers="${CONCURRENCY}" \
    -max-workers="${CONCURRENCY}" \
    -body="${payload_file}" \
    -header="${auth_header}" \
    -header="${content_header}" >"${output_bin}"
  vegeta report "${output_bin}" >"${output_txt}"
  vegeta report -type=json "${output_bin}" >"${output_json}"
}

extract_percentile() {
  local report_file="$1"
  local percentile="$2"
  awk -v p="${percentile}" '$1==p {print $3 " " $4}' "${report_file}" | head -n1
}

extract_requests_per_sec() {
  local report_file="$1"
  awk '$1=="Requests/sec:" {print $2}' "${report_file}" | head -n1
}

rss_bytes() {
  local pid="$1"
  if [[ -f "/proc/${pid}/status" ]]; then
    awk '/VmRSS:/ {print $2*1024; exit}' "/proc/${pid}/status"
    return
  fi
  if command -v ps >/dev/null 2>&1; then
    local rss_kb
    rss_kb="$(ps -o rss= -p "${pid}" | awk '{print $1}' | head -n1 || true)"
    if [[ -n "${rss_kb}" ]]; then
      echo $((rss_kb * 1024))
      return
    fi
  fi
  echo ""
}

peak_rss_bytes=0
stream_pid=""

if [[ "${LOAD_TOOL}" == "hey" ]]; then
  run_hey "${non_stream_payload}" "${OUT_DIR}/nonstream-hey.txt"

  run_hey "${stream_payload}" "${OUT_DIR}/stream-hey.txt" &
  stream_pid=$!

  for _ in $(seq 1 10); do
    if ! kill -0 "${stream_pid}" >/dev/null 2>&1; then
      break
    fi
    current_rss="$(rss_bytes "${stream_pid}" || true)"
    if [[ -n "${current_rss}" ]] && (( current_rss > peak_rss_bytes )); then
      peak_rss_bytes="${current_rss}"
    fi
    sleep 1
  done

  curl -fsS "${PPROF_BASE}/debug/pprof/profile?seconds=30" -o "${OUT_DIR}/cpu.pb.gz"
  curl -fsS "${PPROF_BASE}/debug/pprof/heap" -o "${OUT_DIR}/heap.pb.gz"
  curl -fsS "${PPROF_BASE}/debug/pprof/goroutine?debug=0" -o "${OUT_DIR}/goroutine.pb.gz"
  wait "${stream_pid}"
else
  run_vegeta "${non_stream_payload}" "${OUT_DIR}/nonstream-vegeta.txt" "${OUT_DIR}/nonstream-vegeta.bin" "${OUT_DIR}/nonstream-vegeta.json"
  run_vegeta "${stream_payload}" "${OUT_DIR}/stream-vegeta.txt" "${OUT_DIR}/stream-vegeta.bin" "${OUT_DIR}/stream-vegeta.json" &
  stream_pid=$!

  for _ in $(seq 1 10); do
    if ! kill -0 "${stream_pid}" >/dev/null 2>&1; then
      break
    fi
    current_rss="$(rss_bytes "${stream_pid}" || true)"
    if [[ -n "${current_rss}" ]] && (( current_rss > peak_rss_bytes )); then
      peak_rss_bytes="${current_rss}"
    fi
    sleep 1
  done

  curl -fsS "${PPROF_BASE}/debug/pprof/profile?seconds=30" -o "${OUT_DIR}/cpu.pb.gz"
  curl -fsS "${PPROF_BASE}/debug/pprof/heap" -o "${OUT_DIR}/heap.pb.gz"
  curl -fsS "${PPROF_BASE}/debug/pprof/goroutine?debug=0" -o "${OUT_DIR}/goroutine.pb.gz"
  wait "${stream_pid}"
fi

if [[ "${LOAD_TOOL}" == "hey" ]]; then
  nonstream_report="${OUT_DIR}/nonstream-hey.txt"
  stream_report="${OUT_DIR}/stream-hey.txt"
  nonstream_p50="$(extract_percentile "${nonstream_report}" "50%" || true)"
  nonstream_p95="$(extract_percentile "${nonstream_report}" "95%" || true)"
  nonstream_p99="$(extract_percentile "${nonstream_report}" "99%" || true)"
  stream_p50="$(extract_percentile "${stream_report}" "50%" || true)"
  stream_p95="$(extract_percentile "${stream_report}" "95%" || true)"
  stream_p99="$(extract_percentile "${stream_report}" "99%" || true)"
  nonstream_rps="$(extract_requests_per_sec "${nonstream_report}" || true)"
  stream_rps="$(extract_requests_per_sec "${stream_report}" || true)"
else
  nonstream_report="${OUT_DIR}/nonstream-vegeta.txt"
  stream_report="${OUT_DIR}/stream-vegeta.txt"
  eval "$(
    python - "${OUT_DIR}/nonstream-vegeta.json" "${OUT_DIR}/stream-vegeta.json" <<'PY'
import json
import sys

def fmt_ns(value):
    if value is None:
        return ""
    ns = float(value)
    if ns >= 1_000_000_000:
        return f"{ns/1_000_000_000:.4f} s"
    if ns >= 1_000_000:
        return f"{ns/1_000_000:.4f} ms"
    if ns >= 1_000:
        return f"{ns/1_000:.4f} us"
    return f"{ns:.0f} ns"

def load(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)

def pick_latency(report, key):
    lat = report.get("latencies", {})
    if key in lat:
        return lat[key]
    fallback = {
        "50th": "50",
        "95th": "95",
        "99th": "99",
    }.get(key)
    return lat.get(fallback)

non = load(sys.argv[1])
st = load(sys.argv[2])

print(f"nonstream_p50={fmt_ns(pick_latency(non, '50th'))!r}")
print(f"nonstream_p95={fmt_ns(pick_latency(non, '95th'))!r}")
print(f"nonstream_p99={fmt_ns(pick_latency(non, '99th'))!r}")
print(f"stream_p50={fmt_ns(pick_latency(st, '50th'))!r}")
print(f"stream_p95={fmt_ns(pick_latency(st, '95th'))!r}")
print(f"stream_p99={fmt_ns(pick_latency(st, '99th'))!r}")
print(f"nonstream_rps={str(non.get('throughput', ''))!r}")
print(f"stream_rps={str(st.get('throughput', ''))!r}")
PY
)"
fi

rss_mib="n/a"
if (( peak_rss_bytes > 0 )); then
  rss_mib="$(python - <<PY
value = ${peak_rss_bytes}
print(f"{value / (1024*1024):.2f} MiB")
PY
)"
fi

cat >"${OUT_DIR}/summary.md" <<EOF
# Notion2API perf baseline

- timestamp_utc: ${stamp}
- git_sha: ${git_sha}
- base_url: ${BASE_URL}
- pprof_base: ${PPROF_BASE}
- load_tool: ${LOAD_TOOL}
- concurrency: ${CONCURRENCY}
- duration: ${DURATION}
- peak_rss: ${rss_mib}

## /v1/chat/completions (stream=false)

- p50: ${nonstream_p50}
- p95: ${nonstream_p95}
- p99: ${nonstream_p99}
- requests_per_sec: ${nonstream_rps}

## /v1/chat/completions (stream=true)

- p50: ${stream_p50}
- p95: ${stream_p95}
- p99: ${stream_p99}
- requests_per_sec: ${stream_rps}

## Profiles

- cpu: cpu.pb.gz
- heap: heap.pb.gz
- goroutine: goroutine.pb.gz

## Quick inspect

\`\`\`bash
go tool pprof -top cpu.pb.gz
go tool pprof -top heap.pb.gz
go tool pprof -top goroutine.pb.gz
\`\`\`
EOF

echo "baseline saved to: ${OUT_DIR}"
echo "summary: ${OUT_DIR}/summary.md"
