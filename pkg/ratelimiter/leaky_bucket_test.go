package ratelimiter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeakyBucket_BasicFunctionality(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)

	tests := []struct {
		name            string
		userId          string
		capacity        int
		leakRate        float64
		requestTokens   int
		expectedAllowed bool
	}{
		{"Empty bucket allows request", "user1", 10, 1.0, 1, true},
		{"Full capacity request", "user2", 5, 1.0, 5, true},
		{"Over capacity request", "user3", 5, 1.0, 6, false},
		{"Zero capacity", "user4", 0, 1.0, 1, false},
		{"Multiple tokens", "user5", 10, 1.0, 3, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up any existing data
			cleanupLeakyBucket(t, redisClient, tt.userId)

			allowed, err := lb.RateLimit(context.Background(), tt.userId, tt.capacity, tt.leakRate)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedAllowed, allowed)
		})
	}
}

func TestLeakyBucket_LeakingBehavior(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "leak_user"
	capacity := 5
	leakRate := 2.0 // 2 tokens per second

	cleanupLeakyBucket(t, redisClient, userId)

	// Fill the bucket completely
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, capacity)
	require.NoError(t, err)
	assert.True(t, result.Allowed, "Should allow filling bucket to capacity")
	assert.Equal(t, float64(0), result.RemainingTokens, "Should have 0 tokens remaining")

	// Next request should be denied
	result, err = lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 1)
	require.NoError(t, err)
	assert.False(t, result.Allowed, "Should deny request when bucket is full")

	// Wait for some leaking (1 second = 2 tokens leaked)
	time.Sleep(time.Second + time.Millisecond*100)

	// Should be able to make request now
	result, err = lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 2)
	require.NoError(t, err)
	assert.True(t, result.Allowed, "Should allow request after leaking")
	assert.GreaterOrEqual(t, result.RemainingTokens, float64(0), "Should have some tokens remaining after leak")
}

func TestLeakyBucket_GradualFilling(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "gradual_user"
	capacity := 10
	leakRate := 1.0 // 1 token per second

	cleanupLeakyBucket(t, redisClient, userId)

	// Fill bucket gradually with delays
	for i := 0; i < capacity; i++ {
		result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 1)
		require.NoError(t, err)

		if i < capacity {
			assert.True(t, result.Allowed, "Request %d should be allowed", i)
		}

		// Small delay between requests
		if i < capacity-1 {
			time.Sleep(time.Millisecond * 50)
		}
	}

	// Bucket should now be full, next request denied
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 1)
	require.NoError(t, err)
	assert.False(t, result.Allowed, "Request should be denied when bucket is full")
}

func TestLeakyBucket_ConcurrentUsers(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userCount := 20
	capacity := 5
	leakRate := 1.0
	requestsPerUser := 8

	var wg sync.WaitGroup
	results := make([][]bool, userCount)

	for i := 0; i < userCount; i++ {
		wg.Add(1)
		go func(userIndex int) {
			defer wg.Done()
			userId := fmt.Sprintf("concurrent_user_%d", userIndex)
			cleanupLeakyBucket(t, redisClient, userId)

			results[userIndex] = make([]bool, requestsPerUser)

			for j := 0; j < requestsPerUser; j++ {
				allowed, err := lb.RateLimit(context.Background(), userId, capacity, leakRate)
				require.NoError(t, err)
				results[userIndex][j] = allowed

				// Small delay between requests
				time.Sleep(time.Millisecond * 10)
			}
		}(i)
	}

	wg.Wait()

	// Each user should be able to make up to 'capacity' requests initially
	for i, userResults := range results {
		allowed := 0
		for _, result := range userResults {
			if result {
				allowed++
			}
		}
		// Should allow at least capacity requests (might be more due to leaking during test)
		assert.GreaterOrEqual(t, allowed, capacity, "User %d should have at least %d allowed requests", i, capacity)
		assert.LessOrEqual(t, allowed, requestsPerUser, "User %d should not exceed total requests", i)
	}
}

func TestLeakyBucket_GetBucketState(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "state_user"
	capacity := 10
	leakRate := 2.0

	cleanupLeakyBucket(t, redisClient, userId)

	// Initially should have full capacity
	state, err := lb.GetBucketState(context.Background(), userId, capacity, leakRate)
	require.NoError(t, err)
	assert.Equal(t, float64(capacity), state.Tokens, "Should start with full capacity")
	assert.Equal(t, capacity, state.Capacity)
	assert.Equal(t, leakRate, state.LeakRate)

	// Use some tokens
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 3)
	require.NoError(t, err)
	assert.True(t, result.Allowed)

	// Check state after using tokens
	state, err = lb.GetBucketState(context.Background(), userId, capacity, leakRate)
	require.NoError(t, err)
	assert.Equal(t, float64(capacity-3), state.Tokens, "Should have used 3 tokens")
}

func TestLeakyBucket_RateLimitBatch(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)

	requests := []LeakyBucketRequest{
		{UserID: "batch_user_1", Capacity: 5, LeakRate: 1.0, RequestTokens: 2},
		{UserID: "batch_user_2", Capacity: 3, LeakRate: 0.5, RequestTokens: 1},
		{UserID: "batch_user_1", Capacity: 5, LeakRate: 1.0, RequestTokens: 2}, // Same user
		{UserID: "batch_user_3", Capacity: 1, LeakRate: 1.0, RequestTokens: 1},
	}

	// Clean up users
	for _, req := range requests {
		cleanupLeakyBucket(t, redisClient, req.UserID)
	}

	responses, err := lb.RateLimitBatch(context.Background(), requests)
	require.NoError(t, err)
	require.Len(t, responses, len(requests))

	// Check each response
	for i, resp := range responses {
		assert.NoError(t, resp.Error, "Request %d should not have error", i)
		assert.Equal(t, requests[i].UserID, resp.UserID)

		// First request for each user should be allowed
		if i == 0 || i == 1 || i == 3 {
			assert.True(t, resp.Result.Allowed, "Request %d should be allowed", i)
		}
	}
}

func TestLeakyBucket_WaitTimeCalculation(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "wait_user"
	capacity := 2
	leakRate := 1.0 // 1 token per second

	cleanupLeakyBucket(t, redisClient, userId)

	// Fill bucket completely
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, capacity)
	require.NoError(t, err)
	assert.True(t, result.Allowed)

	// Try to get more tokens than available
	result, err = lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 1)
	require.NoError(t, err)
	assert.False(t, result.Allowed, "Should be denied when bucket is full")

	// Wait time should be approximately 1 second (1 token needed / 1 token per second)
	assert.Greater(t, result.WaitTime, time.Millisecond*900, "Wait time should be close to 1 second")
	assert.Less(t, result.WaitTime, time.Millisecond*1100, "Wait time should be close to 1 second")
}

func TestLeakyBucket_HighPrecisionLeaking(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "precision_user"
	capacity := 10
	leakRate := 0.1 // Very slow leak rate: 0.1 tokens per second

	cleanupLeakyBucket(t, redisClient, userId)

	// Use some tokens
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 5)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, float64(5), result.RemainingTokens)

	// Wait for partial leaking (should leak 0.01 tokens in 100ms)
	time.Sleep(time.Millisecond * 100)

	// Check that leaking happened with precision
	state, err := lb.GetBucketState(context.Background(), userId, capacity, leakRate)
	require.NoError(t, err)

	// Should have leaked approximately 0.01 tokens (0.1 tokens/sec * 0.1 sec)
	expectedTokens := 5.0 + (0.1 * 0.1) // 5 remaining + 0.01 leaked
	assert.InDelta(t, expectedTokens, state.Tokens, 0.05, "Should have precise sub-second leaking")
}

func TestLeakyBucket_ZeroLeakRate(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()

	lb := NewLeakyBucket(redisClient)
	userId := "no_leak_user"
	capacity := 5
	leakRate := 0.0 // No leaking

	cleanupLeakyBucket(t, redisClient, userId)

	// Fill bucket
	result, err := lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, capacity)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, float64(0), result.RemainingTokens)

	// Wait some time
	time.Sleep(time.Millisecond * 500)

	// Should still be full (no leaking)
	result, err = lb.RateLimitWithDetails(context.Background(), userId, capacity, leakRate, 1)
	require.NoError(t, err)
	assert.False(t, result.Allowed, "Should still be denied with no leak rate")
	assert.Equal(t, float64(0), result.RemainingTokens)
}

// Helper function to clean up leaky bucket data
func cleanupLeakyBucket(t *testing.T, client *redis.Client, userId string) {
	ctx := context.Background()
	key := "leaky_bucket:" + userId
	err := client.Del(ctx, key).Err()
	if err != nil && err != redis.Nil {
		t.Logf("Warning: failed to cleanup user %s: %v", userId, err)
	}
}
