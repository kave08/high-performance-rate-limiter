package test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/kave08/high-performance-rate-limiter/pkg/ratelimiter"
)

// BenchmarkSlidingWindow_SingleUser benchmarks sliding window for single user
func BenchmarkSlidingWindow_SingleUser(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.NewSlidingWindow(redisClient, 1)
	userId := "bench_user"
	limit := 1000

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = rl.RateLimit(context.Background(), userId, limit)
		}
	})
}

// BenchmarkSlidingWindow_MultipleUsers benchmarks sliding window with multiple users
func BenchmarkSlidingWindow_MultipleUsers(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.NewSlidingWindow(redisClient, 1)
	userCount := 1000
	limit := 100

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		userIndex := 0
		for pb.Next() {
			userId := fmt.Sprintf("user_%d", userIndex%userCount)
			_, _ = rl.RateLimit(context.Background(), userId, limit)
			userIndex++
		}
	})
}

// BenchmarkLeakyBucket_SingleUser benchmarks leaky bucket for single user
func BenchmarkLeakyBucket_SingleUser(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	lb := ratelimiter.NewLeakyBucket(redisClient)
	userId := "bench_leaky_user"
	capacity := 1000
	leakRate := 100.0

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = lb.RateLimit(context.Background(), userId, capacity, leakRate)
		}
	})
}

// BenchmarkLeakyBucket_MultipleUsers benchmarks leaky bucket with multiple users
func BenchmarkLeakyBucket_MultipleUsers(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	lb := ratelimiter.NewLeakyBucket(redisClient)
	userCount := 1000
	capacity := 100
	leakRate := 10.0

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		userIndex := 0
		for pb.Next() {
			userId := fmt.Sprintf("user_%d", userIndex%userCount)
			_, _ = lb.RateLimit(context.Background(), userId, capacity, leakRate)
			userIndex++
		}
	})
}

// BenchmarkRateLimiter_Throughput benchmarks unified rate limiter throughput
func BenchmarkRateLimiter_Throughput(b *testing.B) {
	scenarios := []struct {
		name       string
		users      int
		limit      int
		algorithm  ratelimiter.Algorithm
	}{
		{"1K users sliding", 1000, 100, ratelimiter.AlgorithmSlidingWindow},
		{"10K users sliding", 10000, 100, ratelimiter.AlgorithmSlidingWindow},
		{"1K users leaky", 1000, 100, ratelimiter.AlgorithmLeakyBucket},
		{"10K users leaky", 10000, 100, ratelimiter.AlgorithmLeakyBucket},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			redisClient := setupBenchmarkRedis(b)
			defer redisClient.Close()

			config := &ratelimiter.Config{
				DefaultAlgorithm: scenario.algorithm,
				DefaultWindow:    time.Second,
				NodeID:           1,
				FallbackPolicy:   ratelimiter.FallbackDeny,
			}

			rl := ratelimiter.NewWithConfig(redisClient, config)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				userIndex := 0
				for pb.Next() {
					userId := fmt.Sprintf("user_%d", userIndex%scenario.users)
					_, _ = rl.RateLimit(context.Background(), userId, scenario.limit)
					userIndex++
				}
			})
		})
	}
}

// BenchmarkRateLimiter_Latency measures latency distribution
func BenchmarkRateLimiter_Latency(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.New(redisClient)
	userId := "latency_user"
	limit := 1000

	// Pre-warm the connection
	for i := 0; i < 10; i++ {
		_, _ = rl.RateLimit(context.Background(), userId, limit)
	}

	latencies := make([]time.Duration, b.N)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, _ = rl.RateLimit(context.Background(), userId, limit)
		latencies[i] = time.Since(start)
	}

	// Calculate percentiles
	if b.N > 0 {
		// Sort for percentile calculation
		for i := 0; i < len(latencies)-1; i++ {
			for j := i + 1; j < len(latencies); j++ {
				if latencies[i] > latencies[j] {
					latencies[i], latencies[j] = latencies[j], latencies[i]
				}
			}
		}

		p50 := latencies[len(latencies)*50/100]
		p95 := latencies[len(latencies)*95/100]
		p99 := latencies[len(latencies)*99/100]

		b.Logf("Latency percentiles - p50: %v, p95: %v, p99: %v", p50, p95, p99)
	}
}

// BenchmarkBatch_Operations benchmarks batch operations
func BenchmarkBatch_Operations(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.NewSlidingWindow(redisClient, 1)
	batchSizes := []int{1, 5, 10, 50, 100}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			requests := make([]ratelimiter.RateLimitRequest, batchSize)
			for i := 0; i < batchSize; i++ {
				requests[i] = ratelimiter.RateLimitRequest{
					UserID: fmt.Sprintf("batch_user_%d", i),
					Limit:  100,
					Window: time.Second,
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = rl.RateLimitBatch(context.Background(), requests)
			}
		})
	}
}

// BenchmarkConcurrency_Scaling tests how performance scales with concurrency
func BenchmarkConcurrency_Scaling(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.New(redisClient)
	concurrencyLevels := []int{1, 10, 50, 100, 200}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("concurrent_%d", concurrency), func(b *testing.B) {
			var wg sync.WaitGroup
			requests := b.N
			requestsPerGoroutine := requests / concurrency

			if requestsPerGoroutine == 0 {
				requestsPerGoroutine = 1
			}

			b.ResetTimer()
			start := time.Now()

			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(goroutineId int) {
					defer wg.Done()
					userId := fmt.Sprintf("concurrent_user_%d", goroutineId)

					for j := 0; j < requestsPerGoroutine; j++ {
						_, _ = rl.RateLimit(context.Background(), userId, 1000)
					}
				}(i)
			}

			wg.Wait()
			duration := time.Since(start)

			actualRequests := concurrency * requestsPerGoroutine
			b.Logf("Concurrency %d: %d requests in %v (%.2f req/sec)",
				concurrency, actualRequests, duration,
				float64(actualRequests)/duration.Seconds())
		})
	}
}

// BenchmarkMemoryUsage measures memory allocation patterns
func BenchmarkMemoryUsage(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	rl := ratelimiter.New(redisClient)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		userId := fmt.Sprintf("memory_user_%d", i%100) // Cycle through 100 users
		_, _ = rl.RateLimit(context.Background(), userId, 100)
	}
}

// BenchmarkRedisConnectionPool tests connection pool performance
func BenchmarkRedisConnectionPool(b *testing.B) {
	poolSizes := []int{5, 10, 20, 50, 100}

	for _, poolSize := range poolSizes {
		b.Run(fmt.Sprintf("pool_%d", poolSize), func(b *testing.B) {
			redisClient := redis.NewClient(&redis.Options{
				Addr:         "localhost:6379",
				DB:           1,
				PoolSize:     poolSize,
				MinIdleConns: poolSize / 4,
			})
			defer redisClient.Close()

			rl := ratelimiter.New(redisClient)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				userIndex := 0
				for pb.Next() {
					userId := fmt.Sprintf("pool_user_%d", userIndex%1000)
					_, _ = rl.RateLimit(context.Background(), userId, 100)
					userIndex++
				}
			})
		})
	}
}

// BenchmarkAlgorithmComparison compares sliding window vs leaky bucket performance
func BenchmarkAlgorithmComparison(b *testing.B) {
	redisClient := setupBenchmarkRedis(b)
	defer redisClient.Close()

	algorithms := []struct {
		name string
		alg  ratelimiter.Algorithm
	}{
		{"sliding_window", ratelimiter.AlgorithmSlidingWindow},
		{"leaky_bucket", ratelimiter.AlgorithmLeakyBucket},
	}

	for _, algorithm := range algorithms {
		b.Run(algorithm.name, func(b *testing.B) {
			config := &ratelimiter.Config{
				DefaultAlgorithm: algorithm.alg,
				DefaultWindow:    time.Second,
				NodeID:           1,
				FallbackPolicy:   ratelimiter.FallbackDeny,
			}

			rl := ratelimiter.NewWithConfig(redisClient, config)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				userIndex := 0
				for pb.Next() {
					userId := fmt.Sprintf("alg_user_%d", userIndex%100)

					if algorithm.alg == ratelimiter.AlgorithmLeakyBucket {
						opts := ratelimiter.RateLimitOptions{
							UserID:    userId,
							Algorithm: algorithm.alg,
							Capacity:  100,
							LeakRate:  10.0,
						}
						_, _ = rl.RateLimitWithOptions(context.Background(), opts)
					} else {
						_, _ = rl.RateLimit(context.Background(), userId, 100)
					}
					userIndex++
				}
			})
		})
	}
}

// setupBenchmarkRedis creates Redis client optimized for benchmarking
func setupBenchmarkRedis(b *testing.B) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr:         "localhost:6379",
		DB:           1, // Use different DB for benchmarks
		PoolSize:     100,
		MinIdleConns: 20,
		DialTimeout:  time.Second * 5,
		ReadTimeout:  time.Millisecond * 100,
		WriteTimeout: time.Millisecond * 100,
	})

	// Test connection
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available for benchmarks: %v", err)
	}

	// Clean up any existing data
	client.FlushDB(ctx)

	return client
}