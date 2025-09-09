package ratelimiter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiter_DefaultBehavior(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	
	// Test basic rate limiting with defaults
	userId := "default_user"
	limit := 5
	
	cleanupUser(t, redisClient, userId)
	
	// Should allow up to limit
	for i := 0; i < limit; i++ {
		allowed, err := rl.RateLimit(context.Background(), userId, limit)
		require.NoError(t, err)
		assert.True(t, allowed, "Request %d should be allowed", i)
	}
	
	// Should deny next request
	allowed, err := rl.RateLimit(context.Background(), userId, limit)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should be denied after limit reached")
}

func TestRateLimiter_SlidingWindowAlgorithm(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	config := &Config{
		DefaultAlgorithm: AlgorithmSlidingWindow,
		DefaultWindow:    time.Millisecond * 200,
		DefaultLimit:     100,
		NodeID:           1,
		FallbackPolicy:   FallbackDeny,
	}
	
	rl := NewWithConfig(redisClient, config)
	userId := "sliding_user"
	limit := 3
	
	cleanupUser(t, redisClient, userId)
	
	// Test with explicit sliding window options
	opts := RateLimitOptions{
		UserID:    userId,
		Limit:     limit,
		Algorithm: AlgorithmSlidingWindow,
		Window:    time.Millisecond * 200,
	}
	
	// Fill the limit
	for i := 0; i < limit; i++ {
		allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
		require.NoError(t, err)
		assert.True(t, allowed, "Request %d should be allowed", i)
	}
	
	// Should be denied
	allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should be denied")
	
	// Wait for window to expire
	time.Sleep(time.Millisecond * 250)
	
	// Should be allowed again
	allowed, err = rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	assert.True(t, allowed, "Request should be allowed after window expires")
}

func TestRateLimiter_LeakyBucketAlgorithm(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	config := &Config{
		DefaultAlgorithm: AlgorithmLeakyBucket,
		DefaultWindow:    time.Second,
		DefaultLimit:     100,
		NodeID:           1,
		FallbackPolicy:   FallbackDeny,
	}
	
	rl := NewWithConfig(redisClient, config)
	userId := "leaky_user"
	capacity := 5
	leakRate := 1.0
	
	cleanupLeakyBucket(t, redisClient, userId)
	
	opts := RateLimitOptions{
		UserID:    userId,
		Limit:     capacity, // Used as capacity for leaky bucket
		Algorithm: AlgorithmLeakyBucket,
		Capacity:  capacity,
		LeakRate:  leakRate,
	}
	
	// Fill the bucket
	for i := 0; i < capacity; i++ {
		allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
		require.NoError(t, err)
		assert.True(t, allowed, "Request %d should be allowed", i)
	}
	
	// Should be denied when full
	allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should be denied when bucket is full")
}

func TestRateLimiter_GetStatsSliding(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	userId := "stats_user"
	window := time.Second
	
	cleanupUser(t, redisClient, userId)
	
	// Make some requests
	for i := 0; i < 3; i++ {
		_, err := rl.RateLimit(context.Background(), userId, 10)
		require.NoError(t, err)
	}
	
	// Get stats
	stats, err := rl.GetStats(context.Background(), userId, AlgorithmSlidingWindow, window)
	require.NoError(t, err)
	
	assert.Equal(t, userId, stats.UserID)
	assert.Equal(t, AlgorithmSlidingWindow, stats.Algorithm)
	assert.Equal(t, 3, stats.CurrentCount)
	assert.Equal(t, window, stats.Window)
}

func TestRateLimiter_GetStatsLeakyBucket(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	userId := "stats_leaky_user"
	
	cleanupLeakyBucket(t, redisClient, userId)
	
	// Make a request first to initialize bucket
	opts := RateLimitOptions{
		UserID:    userId,
		Algorithm: AlgorithmLeakyBucket,
		Capacity:  10,
		LeakRate:  1.0,
	}
	_, err := rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	
	// Get stats
	stats, err := rl.GetStats(context.Background(), userId, AlgorithmLeakyBucket, time.Second)
	require.NoError(t, err)
	
	assert.Equal(t, userId, stats.UserID)
	assert.Equal(t, AlgorithmLeakyBucket, stats.Algorithm)
	assert.Equal(t, 100, stats.Capacity) // Default capacity used in GetStats
	assert.Equal(t, 100.0, stats.LeakRate) // Default leak rate used in GetStats
}

func TestRateLimiter_FallbackPolicy(t *testing.T) {
	tests := []struct {
		name           string
		policy         FallbackPolicy
		expectedResult bool
	}{
		{"Fallback Allow", FallbackAllow, true},
		{"Fallback Deny", FallbackDeny, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a Redis client that will fail
			redisClient := setupTestRedis(t)
			redisClient.Close() // Close immediately to simulate failure
			
			config := &Config{
				DefaultAlgorithm: AlgorithmSlidingWindow,
				DefaultWindow:    time.Second,
				DefaultLimit:     100,
				NodeID:           1,
				FallbackPolicy:   tt.policy,
			}
			
			rl := NewWithConfig(redisClient, config)
			
			// This should trigger fallback behavior
			allowed, err := rl.RateLimit(context.Background(), "test_user", 10)
			
			// Should not return error (handled by fallback)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedResult, allowed)
		})
	}
}

func TestRateLimiter_HealthCheck(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	
	// Health check should pass
	err := rl.HealthCheck(context.Background())
	assert.NoError(t, err)
	
	// Close Redis and health check should fail
	redisClient.Close()
	err = rl.HealthCheck(context.Background())
	assert.Error(t, err)
}

func TestRateLimiter_UnsupportedAlgorithm(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	
	opts := RateLimitOptions{
		UserID:    "test_user",
		Limit:     10,
		Algorithm: "unsupported_algorithm",
	}
	
	allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
	assert.Error(t, err)
	assert.False(t, allowed)
	assert.Contains(t, err.Error(), "unsupported algorithm")
}

func TestRateLimiter_DefaultOptions(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	config := &Config{
		DefaultAlgorithm: AlgorithmLeakyBucket,
		DefaultWindow:    time.Minute,
		DefaultLimit:     50,
		NodeID:           42,
		FallbackPolicy:   FallbackAllow,
	}
	
	rl := NewWithConfig(redisClient, config)
	userId := "default_options_user"
	
	cleanupUser(t, redisClient, userId)
	cleanupLeakyBucket(t, redisClient, userId)
	
	// Use options without specifying algorithm or window
	opts := RateLimitOptions{
		UserID: userId,
		Limit:  10,
		// Algorithm and Window not specified, should use defaults
	}
	
	allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	assert.True(t, allowed, "Should use default algorithm successfully")
}

func TestRateLimiter_LeakyBucketDefaults(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := New(redisClient)
	userId := "leaky_defaults_user"
	
	cleanupLeakyBucket(t, redisClient, userId)
	
	opts := RateLimitOptions{
		UserID:    userId,
		Limit:     5,
		Algorithm: AlgorithmLeakyBucket,
		// Capacity and LeakRate not specified, should use defaults
	}
	
	allowed, err := rl.RateLimitWithOptions(context.Background(), opts)
	require.NoError(t, err)
	assert.True(t, allowed, "Should work with default capacity and leak rate")
}