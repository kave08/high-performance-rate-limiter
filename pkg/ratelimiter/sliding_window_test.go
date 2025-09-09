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

func TestSlidingWindow_BasicFunctionality(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	
	tests := []struct {
		name           string
		userId         string
		limit          int
		requestCount   int
		expectedAllowed int
	}{
		{"Under limit", "user1", 10, 5, 5},
		{"At limit", "user2", 10, 10, 10},
		{"Over limit", "user3", 10, 15, 10},
		{"Zero limit", "user4", 0, 5, 0},
		{"Single request", "user5", 1, 1, 1},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up any existing data
			cleanupUser(t, redisClient, tt.userId)
			
			allowed := 0
			for i := 0; i < tt.requestCount; i++ {
				ok, err := rl.RateLimit(context.Background(), tt.userId, tt.limit)
				require.NoError(t, err)
				if ok {
					allowed++
				}
			}
			
			assert.Equal(t, tt.expectedAllowed, allowed)
		})
	}
}

func TestSlidingWindow_SlidingWindowBehavior(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	userId := "sliding_user"
	limit := 5
	
	// Clean up
	cleanupUser(t, redisClient, userId)
	
	// Fill up the limit
	for i := 0; i < limit; i++ {
		allowed, err := rl.RateLimit(context.Background(), userId, limit)
		require.NoError(t, err)
		assert.True(t, allowed, "Request %d should be allowed", i)
	}
	
	// Next request should be denied
	allowed, err := rl.RateLimit(context.Background(), userId, limit)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should be denied when over limit")
	
	// Wait for sliding window to move (using shorter window for faster test)
	time.Sleep(time.Millisecond * 100)
	
	// Should still be denied immediately after short wait
	allowed, err = rl.RateLimitWithWindow(context.Background(), userId, limit, time.Millisecond*100)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should still be denied in short window")
	
	// Wait for window to slide completely
	time.Sleep(time.Millisecond * 150)
	
	// Should be allowed again after full window slides
	allowed, err = rl.RateLimitWithWindow(context.Background(), userId, limit, time.Millisecond*100)
	require.NoError(t, err)
	assert.True(t, allowed, "Request should be allowed after window slides")
}

func TestSlidingWindow_ConcurrentUsers(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	
	userCount := 50
	requestsPerUser := 10
	limitPerUser := 5
	
	var wg sync.WaitGroup
	results := make([][]bool, userCount)
	
	for i := 0; i < userCount; i++ {
		wg.Add(1)
		go func(userIndex int) {
			defer wg.Done()
			userId := fmt.Sprintf("user_%d", userIndex)
			cleanupUser(t, redisClient, userId)
			
			results[userIndex] = make([]bool, requestsPerUser)
			
			for j := 0; j < requestsPerUser; j++ {
				allowed, err := rl.RateLimit(context.Background(), userId, limitPerUser)
				require.NoError(t, err)
				results[userIndex][j] = allowed
			}
		}(i)
	}
	
	wg.Wait()
	
	// Verify each user got exactly 'limitPerUser' allowed requests
	for i, userResults := range results {
		allowed := 0
		for _, result := range userResults {
			if result {
				allowed++
			}
		}
		assert.Equal(t, limitPerUser, allowed, "User %d should have %d allowed requests", i, limitPerUser)
	}
}

func TestSlidingWindow_ConcurrentSameUser(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	userId := "concurrent_user"
	limit := 10
	goroutines := 20
	requestsPerGoroutine := 5
	
	cleanupUser(t, redisClient, userId)
	
	var wg sync.WaitGroup
	var mu sync.Mutex
	totalAllowed := 0
	
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localAllowed := 0
			
			for j := 0; j < requestsPerGoroutine; j++ {
				allowed, err := rl.RateLimit(context.Background(), userId, limit)
				require.NoError(t, err)
				if allowed {
					localAllowed++
				}
				time.Sleep(time.Millisecond) // Small delay to spread requests
			}
			
			mu.Lock()
			totalAllowed += localAllowed
			mu.Unlock()
		}()
	}
	
	wg.Wait()
	
	// Total allowed should not exceed limit (with small tolerance for timing)
	assert.LessOrEqual(t, totalAllowed, limit+2, "Total allowed requests should not significantly exceed limit")
	assert.GreaterOrEqual(t, totalAllowed, limit-2, "Should allow most requests up to limit")
}

func TestSlidingWindow_GetCurrentCount(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	userId := "count_user"
	window := time.Second
	
	cleanupUser(t, redisClient, userId)
	
	// Initially should be 0
	count, err := rl.GetCurrentCount(context.Background(), userId, window)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	
	// Add some requests
	for i := 0; i < 3; i++ {
		allowed, err := rl.RateLimitWithWindow(context.Background(), userId, 10, window)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
	
	// Count should be 3
	count, err = rl.GetCurrentCount(context.Background(), userId, window)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestSlidingWindow_RateLimitBatch(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	
	requests := []RateLimitRequest{
		{UserID: "batch_user_1", Limit: 5, Window: time.Second},
		{UserID: "batch_user_2", Limit: 3, Window: time.Second},
		{UserID: "batch_user_1", Limit: 5, Window: time.Second}, // Same user again
		{UserID: "batch_user_3", Limit: 1, Window: time.Second},
	}
	
	// Clean up users
	for _, req := range requests {
		cleanupUser(t, redisClient, req.UserID)
	}
	
	responses, err := rl.RateLimitBatch(context.Background(), requests)
	require.NoError(t, err)
	require.Len(t, responses, len(requests))
	
	// All should be allowed on first batch
	for i, resp := range responses {
		assert.NoError(t, resp.Error, "Request %d should not have error", i)
		assert.True(t, resp.Allowed, "Request %d should be allowed", i)
		assert.Equal(t, requests[i].UserID, resp.UserID)
	}
}

func TestSlidingWindow_CustomWindow(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer redisClient.Close()
	
	rl := NewSlidingWindow(redisClient, 1)
	userId := "window_user"
	limit := 3
	customWindow := time.Millisecond * 200
	
	cleanupUser(t, redisClient, userId)
	
	// Fill up the limit
	for i := 0; i < limit; i++ {
		allowed, err := rl.RateLimitWithWindow(context.Background(), userId, limit, customWindow)
		require.NoError(t, err)
		assert.True(t, allowed, "Request %d should be allowed", i)
	}
	
	// Should be rejected now
	allowed, err := rl.RateLimitWithWindow(context.Background(), userId, limit, customWindow)
	require.NoError(t, err)
	assert.False(t, allowed, "Request should be rejected when limit reached")
	
	// Wait for custom window to expire
	time.Sleep(customWindow + time.Millisecond*50)
	
	// Should be allowed again
	allowed, err = rl.RateLimitWithWindow(context.Background(), userId, limit, customWindow)
	require.NoError(t, err)
	assert.True(t, allowed, "Request should be allowed after custom window expires")
}

func TestSlidingWindow_RedisFailure(t *testing.T) {
	redisClient := setupTestRedis(t)
	rl := NewSlidingWindow(redisClient, 1)
	
	// Close the Redis connection to simulate failure
	redisClient.Close()
	
	// Should return error when Redis is unavailable
	allowed, err := rl.RateLimit(context.Background(), "test_user", 10)
	assert.Error(t, err)
	assert.False(t, allowed) // Should default to false on error
}

// Helper functions

func setupTestRedis(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   1, // Use different DB for testing
	})
	
	// Test connection
	ctx := context.Background()
	err := client.Ping(ctx).Err()
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	
	return client
}

func cleanupUser(t *testing.T, client *redis.Client, userId string) {
	ctx := context.Background()
	key := "rate_limit:" + userId
	err := client.Del(ctx, key).Err()
	if err != nil && err != redis.Nil {
		t.Logf("Warning: failed to cleanup user %s: %v", userId, err)
	}
}