package ratelimiter

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// RateLimiter provides a unified interface for different rate limiting algorithms
type RateLimiter struct {
	slidingWindow *SlidingWindowRateLimiter
	leakyBucket   *LeakyBucketRateLimiter
	config        *Config
	fallback      FallbackHandler
}

// Config holds the configuration for the rate limiter
type Config struct {
	DefaultAlgorithm Algorithm
	DefaultWindow    time.Duration
	DefaultLimit     int
	NodeID           uint16
	FallbackPolicy   FallbackPolicy
}

// Algorithm represents the rate limiting algorithm to use
type Algorithm string

const (
	AlgorithmSlidingWindow Algorithm = "sliding_window"
	AlgorithmLeakyBucket   Algorithm = "leaky_bucket"
)

// FallbackPolicy defines behavior when Redis is unavailable
type FallbackPolicy string

const (
	FallbackAllow FallbackPolicy = "allow"  // Fail open - allow all requests
	FallbackDeny  FallbackPolicy = "deny"   // Fail closed - deny all requests
	FallbackLocal FallbackPolicy = "local"  // Use local rate limiting
)

// New creates a new rate limiter with default configuration
func New(redisClient *redis.Client) *RateLimiter {
	return NewWithConfig(redisClient, &Config{
		DefaultAlgorithm: AlgorithmSlidingWindow,
		DefaultWindow:    time.Second,
		DefaultLimit:     100,
		NodeID:           1,
		FallbackPolicy:   FallbackDeny,
	})
}

// NewWithConfig creates a new rate limiter with custom configuration
func NewWithConfig(redisClient *redis.Client, config *Config) *RateLimiter {
	rl := &RateLimiter{
		slidingWindow: NewSlidingWindow(redisClient, config.NodeID),
		leakyBucket:   NewLeakyBucket(redisClient),
		config:        config,
	}
	
	// Initialize fallback handler based on policy
	switch config.FallbackPolicy {
	case FallbackLocal:
		rl.fallback = NewLocalFallback()
	default:
		rl.fallback = NewStaticFallback(config.FallbackPolicy == FallbackAllow)
	}
	
	return rl
}

// RateLimit performs rate limiting using the configured default algorithm
func (rl *RateLimiter) RateLimit(ctx context.Context, userID string, limit int) (bool, error) {
	return rl.RateLimitWithOptions(ctx, RateLimitOptions{
		UserID:    userID,
		Limit:     limit,
		Algorithm: rl.config.DefaultAlgorithm,
		Window:    rl.config.DefaultWindow,
	})
}

// RateLimitOptions contains options for rate limiting
type RateLimitOptions struct {
	UserID     string
	Limit      int
	Algorithm  Algorithm
	Window     time.Duration
	LeakRate   float64 // For leaky bucket algorithm
	Capacity   int     // For leaky bucket algorithm
}

// RateLimitWithOptions performs rate limiting with custom options
func (rl *RateLimiter) RateLimitWithOptions(ctx context.Context, opts RateLimitOptions) (bool, error) {
	// Set defaults
	if opts.Algorithm == "" {
		opts.Algorithm = rl.config.DefaultAlgorithm
	}
	if opts.Window == 0 {
		opts.Window = rl.config.DefaultWindow
	}
	
	var allowed bool
	var err error
	
	switch opts.Algorithm {
	case AlgorithmSlidingWindow:
		allowed, err = rl.slidingWindow.RateLimitWithWindow(ctx, opts.UserID, opts.Limit, opts.Window)
	case AlgorithmLeakyBucket:
		if opts.Capacity == 0 {
			opts.Capacity = opts.Limit
		}
		if opts.LeakRate == 0 {
			opts.LeakRate = float64(opts.Limit) // Default: leak at limit rate per second
		}
		allowed, err = rl.leakyBucket.RateLimit(ctx, opts.UserID, opts.Capacity, opts.LeakRate)
	default:
		return false, fmt.Errorf("unsupported algorithm: %s", opts.Algorithm)
	}
	
	// Handle Redis failures with fallback
	if err != nil {
		return rl.fallback.HandleFailure(ctx, opts.UserID, opts.Limit, err)
	}
	
	return allowed, nil
}

// GetStats returns statistics for a user
func (rl *RateLimiter) GetStats(ctx context.Context, userID string, algorithm Algorithm, window time.Duration) (Stats, error) {
	switch algorithm {
	case AlgorithmSlidingWindow:
		count, err := rl.slidingWindow.GetCurrentCount(ctx, userID, window)
		if err != nil {
			return Stats{}, err
		}
		return Stats{
			UserID:       userID,
			Algorithm:    algorithm,
			CurrentCount: count,
			Window:       window,
		}, nil
		
	case AlgorithmLeakyBucket:
		// For leaky bucket, we need capacity and leak rate - use reasonable defaults
		capacity := 100 // Default capacity
		leakRate := 100.0 // Default leak rate
		
		state, err := rl.leakyBucket.GetBucketState(ctx, userID, capacity, leakRate)
		if err != nil {
			return Stats{}, err
		}
		return Stats{
			UserID:          userID,
			Algorithm:       algorithm,
			RemainingTokens: state.Tokens,
			Capacity:        state.Capacity,
			LeakRate:        state.LeakRate,
		}, nil
		
	default:
		return Stats{}, fmt.Errorf("unsupported algorithm: %s", algorithm)
	}
}

// Stats represents rate limiting statistics
type Stats struct {
	UserID          string
	Algorithm       Algorithm
	CurrentCount    int           // For sliding window
	Window          time.Duration // For sliding window
	RemainingTokens float64       // For leaky bucket
	Capacity        int           // For leaky bucket
	LeakRate        float64       // For leaky bucket
}

// FallbackHandler defines the interface for handling Redis failures
type FallbackHandler interface {
	HandleFailure(ctx context.Context, userID string, limit int, err error) (bool, error)
}

// StaticFallback implements a simple static fallback policy
type StaticFallback struct {
	allowOnFailure bool
}

// NewStaticFallback creates a new static fallback handler
func NewStaticFallback(allowOnFailure bool) *StaticFallback {
	return &StaticFallback{allowOnFailure: allowOnFailure}
}

// HandleFailure implements the FallbackHandler interface
func (sf *StaticFallback) HandleFailure(ctx context.Context, userID string, limit int, err error) (bool, error) {
	return sf.allowOnFailure, nil
}

// LocalFallback implements local in-memory rate limiting as fallback
type LocalFallback struct {
	// Implementation would include local rate limiting logic
	// For brevity, using static behavior here
	allowOnFailure bool
}

// NewLocalFallback creates a new local fallback handler
func NewLocalFallback() *LocalFallback {
	return &LocalFallback{allowOnFailure: false} // Conservative: deny on failure
}

// HandleFailure implements the FallbackHandler interface
func (lf *LocalFallback) HandleFailure(ctx context.Context, userID string, limit int, err error) (bool, error) {
	// In a full implementation, this would use local rate limiting
	// For now, using conservative approach
	return lf.allowOnFailure, fmt.Errorf("redis unavailable, local fallback not implemented: %w", err)
}

// HealthCheck verifies the rate limiter can connect to Redis
func (rl *RateLimiter) HealthCheck(ctx context.Context) error {
	// Try a simple Redis operation
	_, err := rl.slidingWindow.redisClient.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("redis health check failed: %w", err)
	}
	return nil
}