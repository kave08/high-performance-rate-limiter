package ratelimiter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

// LeakyBucketRateLimiter implements leaky bucket rate limiting using Redis Hash
type LeakyBucketRateLimiter struct {
	redisClient *redis.Client
	luaScript   *redis.Script
	keyBuffer   sync.Pool
}

// NewLeakyBucket creates a new leaky bucket rate limiter
func NewLeakyBucket(redisClient *redis.Client) *LeakyBucketRateLimiter {
	return &LeakyBucketRateLimiter{
		redisClient: redisClient,
		luaScript:   redis.NewScript(leakyBucketScript),
		keyBuffer: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 64)
			},
		},
	}
}

// LeakyBucketResult contains detailed information about the rate limit check
type LeakyBucketResult struct {
	Allowed         bool
	RemainingTokens float64
	Capacity        int
	LeakRate        float64
	WaitTime        time.Duration // Time until next token is available
}

// RateLimit checks if a request should be allowed using leaky bucket algorithm
func (lb *LeakyBucketRateLimiter) RateLimit(ctx context.Context, userID string, capacity int, leakRate float64) (bool, error) {
	result, err := lb.RateLimitWithDetails(ctx, userID, capacity, leakRate, 1)
	if err != nil {
		return false, err
	}
	return result.Allowed, nil
}

// RateLimitWithDetails returns detailed bucket state information
func (lb *LeakyBucketRateLimiter) RateLimitWithDetails(ctx context.Context, userID string, capacity int, leakRate float64, requestTokens int) (LeakyBucketResult, error) {
	key := lb.buildKey(userID)
	defer lb.releaseKeyBuffer(key)
	
	currentTime := float64(time.Now().UnixMilli()) / 1000.0 // Convert to seconds with millisecond precision
	
	result, err := lb.luaScript.Run(ctx, lb.redisClient,
		[]string{string(key)},
		capacity, leakRate, currentTime, requestTokens).Result()
	
	if err != nil {
		return LeakyBucketResult{}, fmt.Errorf("redis script execution failed: %w", err)
	}
	
	response := result.([]interface{})
	allowed := response[0].(int64) == 1
	remainingTokens := float64(response[1].(int64)) / 1000.0 // Convert back from Redis integer storage
	
	var waitTime time.Duration
	if !allowed && leakRate > 0 {
		tokensNeeded := float64(requestTokens) - remainingTokens
		waitTime = time.Duration(tokensNeeded/leakRate*1000) * time.Millisecond
	}
	
	return LeakyBucketResult{
		Allowed:         allowed,
		RemainingTokens: remainingTokens,
		Capacity:        capacity,
		LeakRate:        leakRate,
		WaitTime:        waitTime,
	}, nil
}

// RateLimitBatch processes multiple leaky bucket requests
func (lb *LeakyBucketRateLimiter) RateLimitBatch(ctx context.Context, requests []LeakyBucketRequest) ([]LeakyBucketBatchResponse, error) {
	results := make([]LeakyBucketBatchResponse, len(requests))
	var wg sync.WaitGroup
	
	sem := make(chan struct{}, 50) // Limit concurrent operations
	
	for i, req := range requests {
		wg.Add(1)
		go func(idx int, request LeakyBucketRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			
			result, err := lb.RateLimitWithDetails(ctx, request.UserID, request.Capacity, request.LeakRate, request.RequestTokens)
			results[idx] = LeakyBucketBatchResponse{
				UserID: request.UserID,
				Result: result,
				Error:  err,
			}
		}(i, req)
	}
	
	wg.Wait()
	return results, nil
}

// GetBucketState returns the current state of a user's bucket without consuming tokens
func (lb *LeakyBucketRateLimiter) GetBucketState(ctx context.Context, userID string, capacity int, leakRate float64) (BucketState, error) {
	key := lb.buildKey(userID)
	defer lb.releaseKeyBuffer(key)
	
	currentTime := float64(time.Now().UnixMilli()) / 1000.0
	
	result, err := lb.redisClient.Eval(ctx, leakyBucketStateScript,
		[]string{string(key)},
		capacity, leakRate, currentTime).Result()
	
	if err != nil {
		return BucketState{}, fmt.Errorf("failed to get bucket state: %w", err)
	}
	
	response := result.([]interface{})
	tokens := float64(response[0].(int64)) / 1000.0
	lastRefill := response[1].(int64)
	
	return BucketState{
		Tokens:     tokens,
		Capacity:   capacity,
		LeakRate:   leakRate,
		LastRefill: time.Unix(lastRefill, 0),
	}, nil
}

// buildKey constructs the Redis key for a user's bucket
func (lb *LeakyBucketRateLimiter) buildKey(userID string) []byte {
	buf := lb.keyBuffer.Get().([]byte)
	buf = buf[:0]
	buf = append(buf, "leaky_bucket:"...)
	buf = append(buf, userID...)
	return buf
}

// releaseKeyBuffer returns the key buffer to the pool
func (lb *LeakyBucketRateLimiter) releaseKeyBuffer(buf []byte) {
	if cap(buf) <= 128 {
		lb.keyBuffer.Put(buf)
	}
}

// LeakyBucketRequest represents a batch leaky bucket request
type LeakyBucketRequest struct {
	UserID        string
	Capacity      int
	LeakRate      float64
	RequestTokens int
}

// LeakyBucketBatchResponse represents the result of a batch leaky bucket check
type LeakyBucketBatchResponse struct {
	UserID string
	Result LeakyBucketResult
	Error  error
}

// BucketState represents the current state of a leaky bucket
type BucketState struct {
	Tokens     float64
	Capacity   int
	LeakRate   float64
	LastRefill time.Time
}

// Lua script for leaky bucket rate limiting
const leakyBucketScript = `
-- Leaky bucket rate limiting script
local key = KEYS[1]
local capacity = tonumber(ARGV[1])        -- Bucket capacity
local leak_rate = tonumber(ARGV[2])       -- Leak rate (tokens per second)
local current_time = tonumber(ARGV[3])    -- Current timestamp (seconds)
local request_tokens = tonumber(ARGV[4])  -- Tokens requested

-- Get current bucket state (store tokens as integers for precision, multiply by 1000)
local bucket = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(bucket[1]) or (capacity * 1000)  -- Default to full capacity
local last_refill = tonumber(bucket[2]) or current_time

-- Calculate tokens to leak based on time elapsed
local time_elapsed = math.max(0, current_time - last_refill)
local tokens_to_leak = time_elapsed * leak_rate * 1000  -- Convert to integer tokens
tokens = math.max(0, tokens - tokens_to_leak)

-- Convert request tokens to same scale
local request_tokens_scaled = request_tokens * 1000

-- Check if request can be accommodated
if tokens >= request_tokens_scaled then
    -- Allow request and consume tokens
    tokens = tokens - request_tokens_scaled
    
    -- Update bucket state
    redis.call('HMSET', key, 
        'tokens', math.floor(tokens),
        'last_refill', current_time)
    redis.call('EXPIRE', key, 3600) -- 1 hour TTL
    
    return {1, math.floor(tokens)} -- {allowed, remaining_tokens}
else
    -- Reject request but still update timestamp for accurate leaking
    redis.call('HMSET', key,
        'tokens', math.floor(tokens),
        'last_refill', current_time)
    redis.call('EXPIRE', key, 3600)
    
    return {0, math.floor(tokens)} -- {denied, remaining_tokens}
end
`

// Lua script for getting bucket state without modification
const leakyBucketStateScript = `
-- Get leaky bucket state without modification
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local leak_rate = tonumber(ARGV[2])
local current_time = tonumber(ARGV[3])

local bucket = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(bucket[1]) or (capacity * 1000)
local last_refill = tonumber(bucket[2]) or current_time

-- Calculate current tokens after leaking
local time_elapsed = math.max(0, current_time - last_refill)
local tokens_to_leak = time_elapsed * leak_rate * 1000
tokens = math.max(0, tokens - tokens_to_leak)

-- Update state for accuracy
redis.call('HMSET', key, 
    'tokens', math.floor(tokens),
    'last_refill', current_time)

return {math.floor(tokens), last_refill}
`