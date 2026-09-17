// Package telemetry 提供低基数指标和隐私安全的 OpenTelemetry Trace。
package telemetry

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const maxSeriesPerMetric = 1024

// Metric 是允许写入的固定指标名称。
type Metric string

const (
	// RequestsTotal 统计已解析的聊天请求数量。
	RequestsTotal Metric = "limen_chat_requests_total"
	// AttemptsTotal 统计 Provider 尝试及其结果类别。
	AttemptsTotal Metric = "limen_provider_attempts_total"
	// SettlementsTotal 统计请求结算状态。
	SettlementsTotal Metric = "limen_settlements_total"
)

// Labels 是有界的业务标签集合，不包含请求、租户或凭据标识。
type Labels struct {
	Endpoint string
	Status   string
	Model    string
	Provider string
	Target   string
	Result   string
	Reason   string
}

type sample struct {
	labels Labels
	value  uint64
}

// Registry 保存固定指标的内存计数器。
type Registry struct {
	mu      sync.RWMutex
	samples map[Metric]map[string]*sample
}

// NewRegistry 创建空的指标注册表。
func NewRegistry() *Registry {
	return &Registry{samples: make(map[Metric]map[string]*sample)}
}

// Inc 增加一个固定指标，并截断标签值以控制基数和输出大小。
func (registry *Registry) Inc(metric Metric, labels Labels) {
	if registry == nil || !knownMetric(metric) {
		return
	}
	labels = normalizeLabels(labels)
	key := labelsKey(labels)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	series := registry.samples[metric]
	if series == nil {
		series = make(map[string]*sample)
		registry.samples[metric] = series
	}
	if item := series[key]; item != nil {
		item.value++
		return
	}
	if len(series) >= maxSeriesPerMetric {
		return
	}
	series[key] = &sample{labels: labels, value: 1}
}

// ServeHTTP 输出 Prometheus 文本格式指标。
func (registry *Registry) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	writer.WriteHeader(http.StatusOK)
	_, _ = registry.Write(writer)
}

// Write 将当前计数器写入 Prometheus 文本流。
func (registry *Registry) Write(writer io.Writer) (int, error) {
	if registry == nil {
		return 0, nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	metrics := make([]string, 0, len(registry.samples))
	for metric := range registry.samples {
		metrics = append(metrics, string(metric))
	}
	sort.Strings(metrics)
	written := 0
	for _, name := range metrics {
		metric := Metric(name)
		written += writeString(writer, "# TYPE "+name+" counter\n")
		keys := make([]string, 0, len(registry.samples[metric]))
		for key := range registry.samples[metric] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := registry.samples[metric][key]
			line := name + formatLabels(item.labels) + fmt.Sprintf(" %d\n", item.value)
			written += writeString(writer, line)
		}
	}
	return written, nil
}

func knownMetric(metric Metric) bool {
	return metric == RequestsTotal || metric == AttemptsTotal || metric == SettlementsTotal
}

func normalizeLabels(labels Labels) Labels {
	labels.Endpoint = limit(labels.Endpoint)
	labels.Status = limit(labels.Status)
	labels.Model = limit(labels.Model)
	labels.Provider = limit(labels.Provider)
	labels.Target = limit(labels.Target)
	labels.Result = limit(labels.Result)
	labels.Reason = limit(labels.Reason)
	return labels
}

func limit(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

func labelsKey(labels Labels) string {
	return strings.Join([]string{labels.Endpoint, labels.Status, labels.Model, labels.Provider, labels.Target, labels.Result, labels.Reason}, "\x00")
}

func formatLabels(labels Labels) string {
	pairs := []string{
		`endpoint="` + escape(labels.Endpoint) + `"`,
		`status="` + escape(labels.Status) + `"`,
		`model="` + escape(labels.Model) + `"`,
		`provider="` + escape(labels.Provider) + `"`,
		`target="` + escape(labels.Target) + `"`,
		`result="` + escape(labels.Result) + `"`,
		`reason="` + escape(labels.Reason) + `"`,
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func writeString(writer io.Writer, value string) int {
	count, _ := io.WriteString(writer, value)
	return count
}
