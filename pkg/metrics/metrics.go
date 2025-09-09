package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// Metrics provides comprehensive monitoring for the rate limiter
type Metrics struct {
	// Request counters
	RequestsTotal     prometheus.Counter
	RequestsAllowed   prometheus.Counter
	RequestsDenied    prometheus.Counter
	RequestsErrored   prometheus.Counter
	
	// Latency histograms
	RequestDuration   prometheus.Histogram
	RedisDuration     prometheus.Histogram
	
	// Current state gauges
	ActiveUsers       prometheus.Gauge
	RedisConnections  prometheus.Gauge
	
	// Error counters by type
	RedisErrors       *prometheus.CounterVec
	ConfigErrors      prometheus.Counter
	
	// Algorithm-specific metrics
	AlgorithmUsage    *prometheus.CounterVec
	
	// Internal state
	logger        *zap.Logger
	userTracker   *sync.Map // Track active users
	cleanupTicker *time.Ticker
	stopCh        chan struct{}
}

// NewMetrics creates a new metrics instance with Prometheus collectors
func NewMetrics(logger *zap.Logger) *Metrics {
	m := &Metrics{
		RequestsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "rate_limiter_requests_total",
			Help: "Total number of rate limit requests processed",
		}),
		RequestsAllowed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "rate_limiter_requests_allowed_total", 
			Help: "Total number of requests allowed",
		}),
		RequestsDenied: promauto.NewCounter(prometheus.CounterOpts{
			Name: "rate_limiter_requests_denied_total",
			Help: "Total number of requests denied",
		}),
		RequestsErrored: promauto.NewCounter(prometheus.CounterOpts{
			Name: "rate_limiter_requests_errored_total",
			Help: "Total number of requests that resulted in errors",
		}),
		RequestDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name: "rate_limiter_request_duration_seconds",
			Help: "Time spent processing rate limit requests",
			Buckets: []float64{
				0.0001, // 0.1ms
				0.0005, // 0.5ms
				0.001,  // 1ms
				0.002,  // 2ms
				0.005,  // 5ms
				0.01,   // 10ms
				0.025,  // 25ms
				0.05,   // 50ms
				0.1,    // 100ms
				0.25,   // 250ms
				0.5,    // 500ms
			},
		}),
		RedisDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name: "rate_limiter_redis_duration_seconds",
			Help: "Time spent on Redis operations",
			Buckets: []float64{
				0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1,
			},
		}),
		ActiveUsers: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "rate_limiter_active_users",
			Help: "Number of users with active rate limit windows",
		}),
		RedisConnections: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "rate_limiter_redis_connections",
			Help: "Number of active Redis connections",
		}),
		RedisErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "rate_limiter_redis_errors_total",
			Help: "Redis errors by type",
		}, []string{"error_type"}),
		ConfigErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "rate_limiter_config_errors_total",
			Help: "Configuration-related errors",
		}),
		AlgorithmUsage: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "rate_limiter_algorithm_usage_total",
			Help: "Usage of different rate limiting algorithms",
		}, []string{"algorithm", "result"}),
		
		logger:        logger,
		userTracker:   &sync.Map{},
		cleanupTicker: time.NewTicker(time.Minute),
		stopCh:        make(chan struct{}),
	}
	
	// Start background cleanup routine
	go m.cleanupRoutine()
	
	return m
}

// RecordRequest records metrics for a rate limit request
func (m *Metrics) RecordRequest(ctx context.Context, userID, algorithm string, allowed bool, duration time.Duration, err error) {
	m.RequestsTotal.Inc()
	m.RequestDuration.Observe(duration.Seconds())
	
	// Track user activity
	m.userTracker.Store(userID, time.Now())
	
	// Record result
	if err != nil {
		m.RequestsErrored.Inc()
		m.AlgorithmUsage.WithLabelValues(algorithm, "error").Inc()
	} else if allowed {
		m.RequestsAllowed.Inc()
		m.AlgorithmUsage.WithLabelValues(algorithm, "allowed").Inc()
	} else {
		m.RequestsDenied.Inc()
		m.AlgorithmUsage.WithLabelValues(algorithm, "denied").Inc()
	}
}

// RecordRedisOperation records metrics for Redis operations
func (m *Metrics) RecordRedisOperation(duration time.Duration, err error) {
	m.RedisDuration.Observe(duration.Seconds())
	
	if err != nil {
		errorType := classifyRedisError(err)
		m.RedisErrors.WithLabelValues(errorType).Inc()
	}
}

// RecordConfigError records configuration-related errors
func (m *Metrics) RecordConfigError() {
	m.ConfigErrors.Inc()
}

// UpdateActiveUsers updates the active users gauge
func (m *Metrics) UpdateActiveUsers() {
	count := 0
	now := time.Now()
	cutoff := now.Add(-time.Minute * 5) // Consider users active for 5 minutes
	
	m.userTracker.Range(func(key, value interface{}) bool {
		if lastSeen, ok := value.(time.Time); ok && lastSeen.After(cutoff) {
			count++
		}
		return true
	})
	
	m.ActiveUsers.Set(float64(count))
}

// UpdateRedisConnections updates the Redis connections gauge
func (m *Metrics) UpdateRedisConnections(count int) {
	m.RedisConnections.Set(float64(count))
}

// Stop stops the metrics collection and cleanup routines
func (m *Metrics) Stop() {
	close(m.stopCh)
	m.cleanupTicker.Stop()
}

// cleanupRoutine periodically cleans up old user tracking data
func (m *Metrics) cleanupRoutine() {
	for {
		select {
		case <-m.cleanupTicker.C:
			m.cleanupOldUsers()
			m.UpdateActiveUsers()
		case <-m.stopCh:
			return
		}
	}
}

// cleanupOldUsers removes tracking data for inactive users
func (m *Metrics) cleanupOldUsers() {
	cutoff := time.Now().Add(-time.Hour) // Remove users inactive for 1 hour
	
	m.userTracker.Range(func(key, value interface{}) bool {
		if lastSeen, ok := value.(time.Time); ok && lastSeen.Before(cutoff) {
			m.userTracker.Delete(key)
		}
		return true
	})
}

// classifyRedisError categorizes Redis errors for better monitoring
func classifyRedisError(err error) string {
	errStr := err.Error()
	
	switch {
	case contains(errStr, "connection"):
		return "connection"
	case contains(errStr, "timeout"):
		return "timeout"
	case contains(errStr, "auth"):
		return "authentication"
	case contains(errStr, "readonly"):
		return "readonly"
	case contains(errStr, "script"):
		return "script"
	default:
		return "other"
	}
}

// contains checks if a string contains a substring (simple helper)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || 
		   (len(s) > len(substr) && contains(s[1:], substr))
}

// RateLimiterInstrumentor wraps rate limiter operations with metrics collection
type RateLimiterInstrumentor struct {
	metrics *Metrics
}

// NewInstrumentor creates a new instrumentor
func NewInstrumentor(metrics *Metrics) *RateLimiterInstrumentor {
	return &RateLimiterInstrumentor{metrics: metrics}
}

// WrapSlidingWindow returns an instrumented version of sliding window rate limiting
func (i *RateLimiterInstrumentor) WrapSlidingWindow(rateLimitFunc func(ctx context.Context, userID string, limit int) (bool, error)) func(ctx context.Context, userID string, limit int) (bool, error) {
	return func(ctx context.Context, userID string, limit int) (bool, error) {
		start := time.Now()
		allowed, err := rateLimitFunc(ctx, userID, limit)
		duration := time.Since(start)
		
		i.metrics.RecordRequest(ctx, userID, "sliding_window", allowed, duration, err)
		return allowed, err
	}
}

// WrapLeakyBucket returns an instrumented version of leaky bucket rate limiting
func (i *RateLimiterInstrumentor) WrapLeakyBucket(rateLimitFunc func(ctx context.Context, userID string, capacity int, leakRate float64) (bool, error)) func(ctx context.Context, userID string, capacity int, leakRate float64) (bool, error) {
	return func(ctx context.Context, userID string, capacity int, leakRate float64) (bool, error) {
		start := time.Now()
		allowed, err := rateLimitFunc(ctx, userID, capacity, leakRate)
		duration := time.Since(start)
		
		i.metrics.RecordRequest(ctx, userID, "leaky_bucket", allowed, duration, err)
		return allowed, err
	}
}

// HealthChecker provides health check functionality with metrics
type HealthChecker struct {
	metrics      *Metrics
	dependencies map[string]HealthCheckFunc
	logger       *zap.Logger
}

// HealthCheckFunc defines a health check function
type HealthCheckFunc func(ctx context.Context) error

// NewHealthChecker creates a new health checker
func NewHealthChecker(metrics *Metrics, logger *zap.Logger) *HealthChecker {
	return &HealthChecker{
		metrics:      metrics,
		dependencies: make(map[string]HealthCheckFunc),
		logger:       logger,
	}
}

// RegisterDependency registers a dependency for health checking
func (h *HealthChecker) RegisterDependency(name string, checkFunc HealthCheckFunc) {
	h.dependencies[name] = checkFunc
}

// HealthStatus represents the health status of the system
type HealthStatus struct {
	Healthy      bool                       `json:"healthy"`
	Dependencies map[string]DependencyStatus `json:"dependencies"`
	Timestamp    time.Time                  `json:"timestamp"`
}

// DependencyStatus represents the status of a single dependency
type DependencyStatus struct {
	Healthy   bool   `json:"healthy"`
	Error     string `json:"error,omitempty"`
	Latency   string `json:"latency"`
	Timestamp time.Time `json:"timestamp"`
}

// Check performs health checks on all registered dependencies
func (h *HealthChecker) Check(ctx context.Context) HealthStatus {
	status := HealthStatus{
		Healthy:      true,
		Dependencies: make(map[string]DependencyStatus),
		Timestamp:    time.Now(),
	}
	
	for name, checkFunc := range h.dependencies {
		start := time.Now()
		err := checkFunc(ctx)
		latency := time.Since(start)
		
		depStatus := DependencyStatus{
			Healthy:   err == nil,
			Latency:   latency.String(),
			Timestamp: time.Now(),
		}
		
		if err != nil {
			depStatus.Error = err.Error()
			status.Healthy = false
			h.logger.Error("Health check failed", 
				zap.String("dependency", name), 
				zap.Error(err),
				zap.Duration("latency", latency))
		} else {
			h.logger.Debug("Health check passed",
				zap.String("dependency", name),
				zap.Duration("latency", latency))
		}
		
		status.Dependencies[name] = depStatus
	}
	
	return status
}