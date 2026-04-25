package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/logger"
)

type queryMetricsContextKey struct{}

type QueryMetrics struct {
	mu            sync.Mutex
	queryCount    int
	queryDuration time.Duration
	slowestQuery  time.Duration
	stepDurations map[string]time.Duration
}

type QueryMetricsSnapshot struct {
	QueryCount    int
	QueryDuration time.Duration
	SlowestQuery  time.Duration
	Steps         string
}

func NewQueryMetrics() *QueryMetrics {
	return &QueryMetrics{}
}

func WithQueryMetrics(ctx context.Context, metrics *QueryMetrics) context.Context {
	return context.WithValue(ctx, queryMetricsContextKey{}, metrics)
}

func QueryMetricsFromContext(ctx context.Context) (*QueryMetrics, bool) {
	metrics, ok := ctx.Value(queryMetricsContextKey{}).(*QueryMetrics)
	return metrics, ok && metrics != nil
}

type QueryStepTimer struct {
	metrics *QueryMetrics
	name    string
	start   time.Time
}

func StartQueryStep(ctx context.Context, name string) QueryStepTimer {
	metrics, _ := QueryMetricsFromContext(ctx)
	return QueryStepTimer{
		metrics: metrics,
		name:    strings.TrimSpace(name),
		start:   time.Now(),
	}
}

func (t QueryStepTimer) Done() {
	if t.metrics == nil || t.name == "" || t.start.IsZero() {
		return
	}
	t.metrics.RecordStep(t.name, time.Since(t.start))
}

func (m *QueryMetrics) Record(duration time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.queryCount++
	m.queryDuration += duration
	if duration > m.slowestQuery {
		m.slowestQuery = duration
	}
}

func (m *QueryMetrics) RecordStep(name string, duration time.Duration) {
	if m == nil || strings.TrimSpace(name) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stepDurations == nil {
		m.stepDurations = map[string]time.Duration{}
	}
	m.stepDurations[strings.TrimSpace(name)] += duration
}

func (m *QueryMetrics) Snapshot() QueryMetricsSnapshot {
	if m == nil {
		return QueryMetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	return QueryMetricsSnapshot{
		QueryCount:    m.queryCount,
		QueryDuration: m.queryDuration,
		SlowestQuery:  m.slowestQuery,
		Steps:         formatStepDurations(m.stepDurations),
	}
}

func formatStepDurations(steps map[string]time.Duration) string {
	if len(steps) == 0 {
		return ""
	}
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%.1fms", name, float64(steps[name].Microseconds())/1000))
	}
	return strings.Join(parts, ",")
}

type queryMetricsLogger struct {
	logger.Interface
}

func NewQueryMetricsLogger(base logger.Interface) logger.Interface {
	return queryMetricsLogger{Interface: base}
}

func (l queryMetricsLogger) LogMode(level logger.LogLevel) logger.Interface {
	return queryMetricsLogger{Interface: l.Interface.LogMode(level)}
}

func (l queryMetricsLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	if metrics, ok := QueryMetricsFromContext(ctx); ok {
		metrics.Record(elapsed)
	}
	l.Interface.Trace(ctx, begin, fc, err)
}
