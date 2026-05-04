package app

import (
	"expvar"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type histogramSeries struct {
	count   uint64
	sum     float64
	buckets []uint64
}

func newHistogramSeries(bucketCount int) *histogramSeries {
	if bucketCount < 0 {
		bucketCount = 0
	}
	return &histogramSeries{
		buckets: make([]uint64, bucketCount),
	}
}

func (s *histogramSeries) observe(seconds float64, bounds []float64) {
	if s == nil {
		return
	}
	if seconds < 0 {
		seconds = 0
	}
	s.count++
	s.sum += seconds
	for idx, bound := range bounds {
		if seconds <= bound {
			s.buckets[idx]++
		}
	}
}

type requestDurationKey struct {
	Path   string
	Method string
	Status string
}

type sqliteDurationKey struct {
	Op string
}

var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
var transportCallDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
var sqliteOpDurationBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

var (
	requestDurationMu     sync.Mutex
	requestDurationSeries = map[requestDurationKey]*histogramSeries{}

	dispatchInflightMu sync.Mutex
	dispatchInflight   = map[string]int64{}

	transportCallMu     sync.Mutex
	transportCallSeries = newHistogramSeries(len(transportCallDurationBuckets))

	browserSpawnMu    sync.Mutex
	browserSpawnTotal uint64

	browserPoolWorkerMu    sync.Mutex
	browserPoolWorkerTotal uint64

	sqliteDurationMu     sync.Mutex
	sqliteDurationSeries = map[sqliteDurationKey]*histogramSeries{}
)

func resetMetricsForTest() {
	requestDurationMu.Lock()
	requestDurationSeries = map[requestDurationKey]*histogramSeries{}
	requestDurationMu.Unlock()

	dispatchInflightMu.Lock()
	dispatchInflight = map[string]int64{}
	dispatchInflightMu.Unlock()

	transportCallMu.Lock()
	transportCallSeries = newHistogramSeries(len(transportCallDurationBuckets))
	transportCallMu.Unlock()

	browserSpawnMu.Lock()
	browserSpawnTotal = 0
	browserSpawnMu.Unlock()

	browserPoolWorkerMu.Lock()
	browserPoolWorkerTotal = 0
	browserPoolWorkerMu.Unlock()

	sqliteDurationMu.Lock()
	sqliteDurationSeries = map[sqliteDurationKey]*histogramSeries{}
	sqliteDurationMu.Unlock()
}

func observeRequestDuration(path string, method string, status int, elapsed time.Duration) {
	seconds := elapsed.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	key := requestDurationKey{
		Path:   normalizeMetricsPathLabel(path),
		Method: strings.ToUpper(strings.TrimSpace(method)),
		Status: strconv.Itoa(status),
	}
	if key.Method == "" {
		key.Method = "UNKNOWN"
	}
	requestDurationMu.Lock()
	series := requestDurationSeries[key]
	if series == nil {
		series = newHistogramSeries(len(requestDurationBuckets))
		requestDurationSeries[key] = series
	}
	series.observe(seconds, requestDurationBuckets)
	requestDurationMu.Unlock()
}

func setDispatchSlotInflight(email string, inflight int) {
	key := canonicalEmailKey(email)
	if key == "" {
		return
	}
	if inflight < 0 {
		inflight = 0
	}
	dispatchInflightMu.Lock()
	dispatchInflight[key] = int64(inflight)
	dispatchInflightMu.Unlock()
}

func syncDispatchSlotInflightFromSlots(next map[string]*accountSlot) {
	dispatchInflightMu.Lock()
	defer dispatchInflightMu.Unlock()
	for key := range dispatchInflight {
		if _, ok := next[key]; !ok {
			delete(dispatchInflight, key)
		}
	}
	for key, slot := range next {
		if slot == nil {
			continue
		}
		inflight := slot.inflight.Load()
		if inflight < 0 {
			inflight = 0
		}
		dispatchInflight[key] = int64(inflight)
	}
}

func observeTransportCallDuration(elapsed time.Duration) {
	seconds := elapsed.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	transportCallMu.Lock()
	transportCallSeries.observe(seconds, transportCallDurationBuckets)
	transportCallMu.Unlock()
}

func addBrowserHelperSpawn() {
	browserSpawnMu.Lock()
	browserSpawnTotal++
	browserSpawnMu.Unlock()
}

func addBrowserHelperPoolWorkerSpawn() {
	browserPoolWorkerMu.Lock()
	browserPoolWorkerTotal++
	browserPoolWorkerMu.Unlock()
}

func observeSQLiteOpDuration(op string, elapsed time.Duration) {
	op = strings.TrimSpace(strings.ToLower(op))
	if op == "" {
		op = "unknown"
	}
	seconds := elapsed.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	key := sqliteDurationKey{Op: op}
	sqliteDurationMu.Lock()
	series := sqliteDurationSeries[key]
	if series == nil {
		series = newHistogramSeries(len(sqliteOpDurationBuckets))
		sqliteDurationSeries[key] = series
	}
	series.observe(seconds, sqliteOpDurationBuckets)
	sqliteDurationMu.Unlock()
}

func normalizeMetricsPathLabel(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "unknown"
	}
	switch {
	case clean == "/":
		return "/"
	case clean == "/healthz":
		return "/healthz"
	case clean == "/metrics":
		return "/metrics"
	case clean == "/debug/vars":
		return "/debug/vars"
	case strings.HasPrefix(clean, "/v1/models/"):
		return "/v1/models/:id"
	case clean == "/v1/models":
		return "/v1/models"
	case strings.HasPrefix(clean, "/v1/responses/"):
		return "/v1/responses/:id"
	case clean == "/v1/responses":
		return "/v1/responses"
	case clean == "/v1/chat/completions":
		return "/v1/chat/completions"
	case clean == "/v1/st/chat/completions":
		return "/v1/st/chat/completions"
	case strings.HasPrefix(clean, "/admin/accounts/"):
		return "/admin/accounts/:id"
	case strings.HasPrefix(clean, "/admin/conversations/"):
		return "/admin/conversations/:id"
	case strings.HasPrefix(clean, "/admin"):
		return "/admin/*"
	}
	if strings.Count(clean, "/") >= 2 {
		parts := strings.Split(clean, "/")
		if len(parts) >= 3 {
			return "/" + parts[1] + "/" + parts[2] + "/*"
		}
	}
	return clean
}

func writePrometheusMetrics(w http.ResponseWriter) {
	if w == nil {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_request_duration_seconds HTTP request duration seconds by path/method/status.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_request_duration_seconds histogram")
	writeRequestDurationHistogram(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_dispatch_slot_inflight Current in-flight dispatch slots per account email.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_dispatch_slot_inflight gauge")
	writeDispatchInflightGauge(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_transport_call_duration_seconds Duration of transport helper calls in seconds.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_transport_call_duration_seconds histogram")
	writeTransportCallHistogram(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_browser_helper_spawn_total Total spawned browser helper subprocesses.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_browser_helper_spawn_total counter")
	writeBrowserHelperSpawnCounter(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_browser_helper_pool_worker_spawn_total Total spawned persistent browser helper pool workers.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_browser_helper_pool_worker_spawn_total counter")
	writeBrowserHelperPoolWorkerSpawnCounter(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_sqlite_op_duration_seconds SQLite operation durations in seconds by operation.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_sqlite_op_duration_seconds histogram")
	writeSQLiteDurationHistogram(w)

	_, _ = fmt.Fprintln(w, "# HELP notion2api_response_store_prune_total Total number of pruned in-memory response entries by reason.")
	_, _ = fmt.Fprintln(w, "# TYPE notion2api_response_store_prune_total counter")
	writeResponseStorePruneCounter(w)
}

func writeRequestDurationHistogram(w http.ResponseWriter) {
	requestDurationMu.Lock()
	seriesMap := make(map[requestDurationKey]*histogramSeries, len(requestDurationSeries))
	keys := make([]requestDurationKey, 0, len(requestDurationSeries))
	for key, series := range requestDurationSeries {
		copySeries := *series
		copySeries.buckets = append([]uint64(nil), series.buckets...)
		seriesMap[key] = &copySeries
		keys = append(keys, key)
	}
	requestDurationMu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Path != keys[j].Path {
			return keys[i].Path < keys[j].Path
		}
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		return keys[i].Status < keys[j].Status
	})

	for _, key := range keys {
		series := seriesMap[key]
		if series == nil {
			continue
		}
		labelPrefix := fmt.Sprintf("path=\"%s\",method=\"%s\",status=\"%s\"",
			escapePrometheusLabelValue(key.Path),
			escapePrometheusLabelValue(key.Method),
			escapePrometheusLabelValue(key.Status),
		)
		writeHistogramSeries(w, "notion2api_request_duration_seconds", labelPrefix, requestDurationBuckets, series)
	}
}

func writeDispatchInflightGauge(w http.ResponseWriter) {
	dispatchInflightMu.Lock()
	type pair struct {
		email string
		value int64
	}
	items := make([]pair, 0, len(dispatchInflight))
	for email, value := range dispatchInflight {
		items = append(items, pair{email: email, value: value})
	}
	dispatchInflightMu.Unlock()

	sort.Slice(items, func(i, j int) bool { return items[i].email < items[j].email })
	for _, item := range items {
		_, _ = fmt.Fprintf(w, "notion2api_dispatch_slot_inflight{email=\"%s\"} %d\n",
			escapePrometheusLabelValue(item.email), item.value)
	}
}

func writeTransportCallHistogram(w http.ResponseWriter) {
	transportCallMu.Lock()
	series := *transportCallSeries
	series.buckets = append([]uint64(nil), transportCallSeries.buckets...)
	transportCallMu.Unlock()
	writeHistogramSeries(w, "notion2api_transport_call_duration_seconds", "", transportCallDurationBuckets, &series)
}

func writeBrowserHelperSpawnCounter(w http.ResponseWriter) {
	browserSpawnMu.Lock()
	total := browserSpawnTotal
	browserSpawnMu.Unlock()
	_, _ = fmt.Fprintf(w, "notion2api_browser_helper_spawn_total %d\n", total)
}

func writeBrowserHelperPoolWorkerSpawnCounter(w http.ResponseWriter) {
	browserPoolWorkerMu.Lock()
	total := browserPoolWorkerTotal
	browserPoolWorkerMu.Unlock()
	_, _ = fmt.Fprintf(w, "notion2api_browser_helper_pool_worker_spawn_total %d\n", total)
}

func writeSQLiteDurationHistogram(w http.ResponseWriter) {
	sqliteDurationMu.Lock()
	seriesMap := make(map[sqliteDurationKey]*histogramSeries, len(sqliteDurationSeries))
	keys := make([]sqliteDurationKey, 0, len(sqliteDurationSeries))
	for key, series := range sqliteDurationSeries {
		copySeries := *series
		copySeries.buckets = append([]uint64(nil), series.buckets...)
		seriesMap[key] = &copySeries
		keys = append(keys, key)
	}
	sqliteDurationMu.Unlock()

	sort.Slice(keys, func(i, j int) bool { return keys[i].Op < keys[j].Op })
	for _, key := range keys {
		series := seriesMap[key]
		if series == nil {
			continue
		}
		labelPrefix := fmt.Sprintf("op=\"%s\"", escapePrometheusLabelValue(key.Op))
		writeHistogramSeries(w, "notion2api_sqlite_op_duration_seconds", labelPrefix, sqliteOpDurationBuckets, series)
	}
}

func writeResponseStorePruneCounter(w http.ResponseWriter) {
	if w == nil {
		return
	}
	entryVar := responseStorePruneTotalMetric.Get("expired_entries")
	if entryVar == nil {
		_, _ = fmt.Fprintln(w, "notion2api_response_store_prune_total{reason=\"expired_entries\"} 0")
		return
	}
	entryValue, ok := entryVar.(*expvar.Int)
	if !ok || entryValue == nil {
		_, _ = fmt.Fprintln(w, "notion2api_response_store_prune_total{reason=\"expired_entries\"} 0")
		return
	}
	_, _ = fmt.Fprintf(w, "notion2api_response_store_prune_total{reason=\"expired_entries\"} %d\n", entryValue.Value())
}

func writeHistogramSeries(w http.ResponseWriter, metricName string, baseLabels string, bounds []float64, series *histogramSeries) {
	if w == nil || series == nil {
		return
	}
	for idx, bound := range bounds {
		le := strconv.FormatFloat(bound, 'g', -1, 64)
		labels := withExtraLabel(baseLabels, "le", le)
		_, _ = fmt.Fprintf(w, "%s_bucket{%s} %d\n", metricName, labels, series.buckets[idx])
	}
	infLabels := withExtraLabel(baseLabels, "le", "+Inf")
	_, _ = fmt.Fprintf(w, "%s_bucket{%s} %d\n", metricName, infLabels, series.count)
	if baseLabels == "" {
		_, _ = fmt.Fprintf(w, "%s_sum %s\n", metricName, formatFloat(series.sum))
		_, _ = fmt.Fprintf(w, "%s_count %d\n", metricName, series.count)
		return
	}
	_, _ = fmt.Fprintf(w, "%s_sum{%s} %s\n", metricName, baseLabels, formatFloat(series.sum))
	_, _ = fmt.Fprintf(w, "%s_count{%s} %d\n", metricName, baseLabels, series.count)
}

func withExtraLabel(base string, name string, value string) string {
	extra := fmt.Sprintf("%s=\"%s\"", name, escapePrometheusLabelValue(value))
	if strings.TrimSpace(base) == "" {
		return extra
	}
	return base + "," + extra
}

func escapePrometheusLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
