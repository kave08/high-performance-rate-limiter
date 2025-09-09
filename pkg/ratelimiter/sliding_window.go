package ratelimiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
)

// SlidingWindowRateLimiter implements precise sliding window rate limiting using Redis Sorted Sets
type SlidingWindowRateLimiter struct {
	redisClient *redis.Client
	luaScript   *redis.Script
	nodeID      uint16
	counter     uint64
	keyBuffer   sync.Pool
}

// NewSlidingWindow creates a new sliding window rate limiter
func NewSlidingWindow(redisClient *redis.Client, nodeID uint16) *SlidingWindowRateLimiter {
	return &SlidingWindowRateLimiter{
		redisClient: redisClient,
		luaScript:   redis.NewScript(slidingWindowScript),
		nodeID:      nodeID,
		keyBuffer: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 64)
			},
		},
	}
}

// RateLimit checks if a request should be allowed based on sliding window algorithm
func (sw *SlidingWindowRateLimiter) RateLimit(ctx context.Context, userID string, limit int) (bool, error) {
	return sw.RateLimitWithWindow(ctx, userID, limit, time.Second)
}

// RateLimitWithWindow allows custom window duration
func (sw *SlidingWindowRateLimiter) RateLimitWithWindow(ctx context.Context, userID string, limit int, window time.Duration) (bool, error) {
	now := time.Now().UnixMilli()
	windowStart := now - window.Milliseconds()
	requestID := sw.generateRequestID()

	key := sw.buildKey(userID)
	defer sw.releaseKeyBuffer(key)

	result, err := sw.luaScript.Run(ctx, sw.redisClient,
		[]string{string(key)},
		windowStart, now, limit, requestID).Int()

	if err != nil {
		return false, fmt.Errorf("redis script execution failed: %w", err)
	}

	return result == 1, nil
}

// RateLimitBatch processes multiple rate limit requests efficiently
func (sw *SlidingWindowRateLimiter) RateLimitBatch(ctx context.Context, requests []RateLimitRequest) ([]RateLimitResponse, error) {
	results := make([]RateLimitResponse, len(requests))
	var wg sync.WaitGroup

	// Semaphore to limit concurrent Redis operations
	sem := make(chan struct{}, 50)

	for i, req := range requests {
		wg.Add(1)
		go func(idx int, request RateLimitRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			allowed, err := sw.RateLimitWithWindow(ctx, request.UserID, request.Limit, request.Window)
			results[idx] = RateLimitResponse{
				UserID:  request.UserID,
				Allowed: allowed,
				Error:   err,
			}
		}(i, req)
	}

	wg.Wait()
	return results, nil
}

// GetCurrentCount returns the current request count for a user without modifying state
func (sw *SlidingWindowRateLimiter) GetCurrentCount(ctx context.Context, userID string, window time.Duration) (int, error) {
	now := time.Now().UnixMilli()
	windowStart := now - window.Milliseconds()

	key := sw.buildKey(userID)
	defer sw.releaseKeyBuffer(key)

	result, err := sw.redisClient.Eval(ctx, slidingWindowCountScript,
		[]string{string(key)},
		windowStart).Int()

	if err != nil {
		return 0, fmt.Errorf("failed to get current count: %w", err)
	}

	return result, nil
}

// generateRequestID creates a unique request identifier
func (sw *SlidingWindowRateLimiter) generateRequestID() string {
	count := atomic.AddUint64(&sw.counter, 1)
	now := time.Now().UnixMilli()
	return fmt.Sprintf("%d_%d_%d", now, sw.nodeID, count)
}

// buildKey constructs the Redis key for a user
func (sw *SlidingWindowRateLimiter) buildKey(userID string) []byte {
	buf := sw.keyBuffer.Get().([]byte)
	buf = buf[:0]
	buf = append(buf, "rate_limit:"...)
	buf = append(buf, userID...)
	return buf
}

// releaseKeyBuffer returns the key buffer to the pool
func (sw *SlidingWindowRateLimiter) releaseKeyBuffer(buf []byte) {
	if cap(buf) <= 128 { // Prevent memory bloat
		sw.keyBuffer.Put(buf)
	}
}

// RateLimitRequest represents a batch rate limit request
type RateLimitRequest struct {
	UserID string
	Limit  int
	Window time.Duration
}

// RateLimitResponse represents the result of a rate limit check
type RateLimitResponse struct {
	UserID  string
	Allowed bool
	Error   error
}

// Lua script for atomic sliding window rate limiting
const slidingWindowScript = `
-- Sliding window rate limiting script
local key = KEYS[1]              -- "rate_limit:{userId}"
local window_start = ARGV[1]     -- current_time - window_ms
local current_time = ARGV[2]     -- current timestamp (milliseconds)
local limit = tonumber(ARGV[3])  -- rate limit
local request_id = ARGV[4]       -- unique request identifier

-- Remove expired entries outside the sliding window
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- Count current requests in the window
local current_count = redis.call('ZCARD', key)

-- Check if request can be allowed
if current_count < limit then
    -- Add new request to the window
    redis.call('ZADD', key, current_time, request_id)
    -- Set TTL to prevent memory leaks (2x window size for safety)
    redis.call('EXPIRE', key, 2)
    return 1  -- Allow request
else
    -- Reject request but still set TTL for cleanup
    redis.call('EXPIRE', key, 2)
    return 0  -- Reject request
end
`

// Lua script for counting requests without modification
const slidingWindowCountScript = `
-- Count requests in sliding window without modification
local key = KEYS[1]
local window_start = ARGV[1]

-- Clean up expired entries
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- Return current count
return redis.call('ZCARD', key)
`
