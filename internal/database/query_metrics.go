package database

import (
	"context"
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
}

type QueryMetricsSnapshot struct {
	QueryCount    int
	QueryDuration time.Duration
	SlowestQuery  time.Duration
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
	}
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
