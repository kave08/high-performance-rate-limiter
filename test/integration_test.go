package test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kave08/high-performance-rate-limiter/pkg/ratelimiter"
)

// TestDistributedConsistency tests that rate limiting works consistently across multiple instances
func TestDistributedConsistency(t *testing.T) {
	redisContainer := setupRedisContainer(t)
	defer redisContainer.Terminate(context.Background())

	redisAddr := getRedisAddress(t, redisContainer)

	// Create multiple rate limiter instances
	instances := make([]*ratelimiter.RateLimiter, 3)
	for i := range instances {
		client := redis.NewClient(&redis.Options{Addr: redisAddr})
		instances[i] = ratelimiter.NewWithConfig(client, &ratelimiter.Config{
			DefaultAlgorithm: ratelimiter.AlgorithmSlidingWindow,
			DefaultWindow:    time.Second,
			NodeID:           uint16(i + 1),
			FallbackPolicy:   ratelimiter.FallbackDeny,
		})
	}

	userId := "distributed_user"
	globalLimit := 10
	requestsPerInstance := 20

	var wg sync.WaitGroup
	allResults := make([][]bool, len(instances))

	// Send requests from multiple instances simultaneously
	for i, instance := range instances {
		wg.Add(1)
		go func(idx int, rl *ratelimiter.RateLimiter) {
			defer wg.Done()
			results := make([]bool, requestsPerInstance)

			for j := 0; j < requestsPerInstance; j++ {
				allowed, err := rl.RateLimit(context.Background(), userId, globalLimit)
				require.NoError(t, err)
				results[j] = allowed
				time.Sleep(time.Millisecond * 5) // Small delay to simulate real requests
			}

			allResults[idx] = results
		}(i, instance)
	}

	wg.Wait()

	// Count total allowed requests across all instances
	totalAllowed := 0
	for _, instanceResults := range allResults {
		for _, allowed := range instanceResults {
			if allowed {
				totalAllowed++
			}
		}
	}

	// Should not exceed global limit by more than a small tolerance
	// (allowing for minor timing variations in distributed system)
	assert.LessOrEqual(t, totalAllowed, globalLimit+3,
		"Global limit should be respected across instances (got %d, limit %d)",
		totalAllowed, globalLimit)
	assert.GreaterOrEqual(t, totalAllowed, globalLimit-1,
		"Should allow most requests up to limit")

	t.Logf("Total allowed requests: %d (limit: %d)", totalAllowed, globalLimit)
}

// TestHighConcurrencyDistributed tests high concurrency across multiple instances
func TestHighConcurrencyDistributed(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high concurrency test in short mode")
	}

	redisContainer := setupRedisContainer(t)
	defer redisContainer.Terminate(context.Background())

	redisAddr := getRedisAddress(t, redisContainer)

	// Create multiple instances
	instanceCount := 5
	goroutinesPerInstance := 20
	requestsPerGoroutine := 10
	instances := make([]*ratelimiter.RateLimiter, instanceCount)

	for i := range instances {
		client := redis.NewClient(&redis.Options{
			Addr:         redisAddr,
			PoolSize:     20,
			MinIdleConns: 5,
		})
		instances[i] = ratelimiter.NewWithConfig(client, &ratelimiter.Config{
			DefaultAlgorithm: ratelimiter.AlgorithmSlidingWindow,
			DefaultWindow:    time.Second,
			NodeID:           uint16(i + 1),
			FallbackPolicy:   ratelimiter.FallbackDeny,
		})
	}

	userId := "high_concurrency_user"
	limit := 50

	var wg sync.WaitGroup
	var totalAllowed int64
	var mu sync.Mutex

	startTime := time.Now()

	// Launch goroutines across all instances
	for i, instance := range instances {
		for g := 0; g < goroutinesPerInstance; g++ {
			wg.Add(1)
			go func(instanceIdx, goroutineIdx int, rl *ratelimiter.RateLimiter) {
				defer wg.Done()

				localAllowed := 0
				for r := 0; r < requestsPerGoroutine; r++ {
					allowed, err := rl.RateLimit(context.Background(), userId, limit)
					if err != nil {
						t.Logf("Error in instance %d, goroutine %d: %v", instanceIdx, goroutineIdx, err)
						continue
					}
					if allowed {
						localAllowed++
					}
				}

				mu.Lock()
				totalAllowed += int64(localAllowed)
				mu.Unlock()
			}(i, g, instance)
		}
	}

	wg.Wait()
	duration := time.Since(startTime)

	totalRequests := int64(instanceCount * goroutinesPerInstance * requestsPerGoroutine)

	t.Logf("High concurrency test completed:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total requests: %d", totalRequests)
	t.Logf("  Allowed requests: %d", totalAllowed)
	t.Logf("  Throughput: %.2f requests/sec", float64(totalRequests)/duration.Seconds())

	// Verify rate limiting worked
	assert.LessOrEqual(t, totalAllowed, int64(limit+5), "Should not significantly exceed limit")
	assert.GreaterOrEqual(t, totalAllowed, int64(limit-5), "Should allow most requests up to limit")
}

// TestLeakyBucketDistributed tests leaky bucket algorithm across instances
func TestLeakyBucketDistributed(t *testing.T) {
	redisContainer := setupRedisContainer(t)
	defer redisContainer.Terminate(context.Background())

	redisAddr := getRedisAddress(t, redisContainer)

	// Create multiple instances
	instances := make([]*ratelimiter.RateLimiter, 3)
	for i := range instances {
		client := redis.NewClient(&redis.Options{Addr: redisAddr})
		instances[i] = ratelimiter.NewWithConfig(client, &ratelimiter.Config{
			DefaultAlgorithm: ratelimiter.AlgorithmLeakyBucket,
			NodeID:           uint16(i + 1),
			FallbackPolicy:   ratelimiter.FallbackDeny,
		})
	}

	userId := "leaky_distributed_user"
	capacity := 10
	leakRate := 2.0 // 2 tokens per second

	opts := ratelimiter.RateLimitOptions{
		UserID:    userId,
		Algorithm: ratelimiter.AlgorithmLeakyBucket,
		Capacity:  capacity,
		LeakRate:  leakRate,
	}

	var wg sync.WaitGroup
	allowedPerInstance := make([]int, len(instances))

	// Make requests from multiple instances
	for i, instance := range instances {
		wg.Add(1)
		go func(idx int, rl *ratelimiter.RateLimiter) {
			defer wg.Done()

			allowed := 0
			for j := 0; j < 15; j++ { // More requests than capacity
				result, err := rl.RateLimitWithOptions(context.Background(), opts)
				require.NoError(t, err)
				if result {
					allowed++
				}
				time.Sleep(time.Millisecond * 10)
			}

			allowedPerInstance[idx] = allowed
		}(i, instance)
	}

	wg.Wait()

	totalAllowed := 0
	for _, allowed := range allowedPerInstance {
		totalAllowed += allowed
	}

	// With leaky bucket, should allow approximately capacity + some leaking
	// during the test duration
	expectedMin := capacity
	expectedMax := capacity + 10 // Allow for some leaking during test

	assert.GreaterOrEqual(t, totalAllowed, expectedMin,
		"Should allow at least capacity requests")
	assert.LessOrEqual(t, totalAllowed, expectedMax,
		"Should not allow too many more than capacity")

	t.Logf("Leaky bucket distributed test - Total allowed: %d (capacity: %d)",
		totalAllowed, capacity)
}

// TestRedisConnectionFailure tests behavior when Redis connections fail
func TestRedisConnectionFailure(t *testing.T) {
	redisContainer := setupRedisContainer(t)
	redisAddr := getRedisAddress(t, redisContainer)

	// Create rate limiter with fallback policy
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	rl := ratelimiter.NewWithConfig(client, &ratelimiter.Config{
		DefaultAlgorithm: ratelimiter.AlgorithmSlidingWindow,
		FallbackPolicy:   ratelimiter.FallbackAllow, // Fail open
	})

	// Test normal operation first
	allowed, err := rl.RateLimit(context.Background(), "test_user", 10)
	require.NoError(t, err)
	assert.True(t, allowed)

	// Terminate Redis container to simulate failure
	redisContainer.Terminate(context.Background())

	// Wait a moment for connection to fail
	time.Sleep(time.Millisecond * 100)

	// Should use fallback policy (allow)
	allowed, err = rl.RateLimit(context.Background(), "test_user", 10)
	assert.NoError(t, err)  // Fallback should not return error
	assert.True(t, allowed) // Should allow due to FallbackAllow policy
}

// TestMultipleUsersConcurrent tests multiple users with concurrent access
func TestMultipleUsersConcurrent(t *testing.T) {
	redisContainer := setupRedisContainer(t)
	defer redisContainer.Terminate(context.Background())

	redisAddr := getRedisAddress(t, redisContainer)

	// Create single rate limiter instance
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	rl := ratelimiter.New(client)

	userCount := 100
	limit := 5
	requestsPerUser := 10

	var wg sync.WaitGroup
	results := make([]int, userCount) // Track allowed requests per user

	// Launch goroutines for each user
	for i := 0; i < userCount; i++ {
		wg.Add(1)
		go func(userIdx int) {
			defer wg.Done()

			userId := fmt.Sprintf("user_%d", userIdx)
			allowed := 0

			for j := 0; j < requestsPerUser; j++ {
				result, err := rl.RateLimit(context.Background(), userId, limit)
				if err != nil {
					t.Errorf("Error for user %s: %v", userId, err)
					continue
				}
				if result {
					allowed++
				}
				time.Sleep(time.Microsecond * 100) // Small delay
			}

			results[userIdx] = allowed
		}(i)
	}

	wg.Wait()

	// Verify each user got appropriate number of allowed requests
	for i, allowed := range results {
		assert.Equal(t, limit, allowed,
			"User %d should have exactly %d allowed requests, got %d",
			i, limit, allowed)
	}

	t.Logf("Multiple users test completed - %d users with %d requests each",
		userCount, requestsPerUser)
}

// Helper functions

func setupRedisContainer(t *testing.T) testcontainers.Container {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	return container
}

func getRedisAddress(t *testing.T, container testcontainers.Container) string {
	ctx := context.Background()

	host, err := container.Host(ctx)
	require.NoError(t, err)

	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	return fmt.Sprintf("%s:%s", host, port.Port())
}
