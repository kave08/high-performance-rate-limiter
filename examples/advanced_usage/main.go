package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/kave08/high-performance-rate-limiter/pkg/config"
	"github.com/kave08/high-performance-rate-limiter/pkg/metrics"
	"github.com/kave08/high-performance-rate-limiter/pkg/ratelimiter"
	"github.com/kave08/high-performance-rate-limiter/pkg/repository"
	"github.com/kave08/high-performance-rate-limiter/pkg/services"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer func() {
		if err := logger.Sync(); err != nil {
			log.Printf("Logger sync error: %v", err)
		}
	}()

	ctx := context.Background()

	// Setup Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr:         "localhost:6379",
		PoolSize:     50,
		MinIdleConns: 10,
		MaxRetries:   3,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer redisClient.Close()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	logger.Info("Connected to Redis successfully")

	// Example 1: Advanced Configuration
	fmt.Println("🛠️  Example 1: Advanced Rate Limiter Configuration")
	fmt.Println("===================================================")

	advancedConfig := &ratelimiter.Config{
		DefaultAlgorithm: ratelimiter.AlgorithmSlidingWindow,
		DefaultWindow:    time.Second,
		DefaultLimit:     100,
		NodeID:           1,
		FallbackPolicy:   ratelimiter.FallbackDeny,
	}

	limiter := ratelimiter.NewWithConfig(redisClient, advancedConfig)

	// Test different algorithms
	testAlgorithms(ctx, limiter, logger)

	// Example 2: Metrics and Monitoring
	fmt.Println("\n📊 Example 2: Metrics and Monitoring")
	fmt.Println("====================================")

	metricsSystem := metrics.NewMetrics(logger)
	defer metricsSystem.Stop()

	// Start metrics HTTP server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		log.Println("Metrics server starting on :2112")
		log.Fatal(http.ListenAndServe(":2112", nil))
	}()

	// Instrument rate limiter
	instrumentor := metrics.NewInstrumentor(metricsSystem)

	// Create instrumented functions
	instrumentedSlidingWindow := instrumentor.WrapSlidingWindow(limiter.RateLimit)

	// Generate some metrics data
	generateMetricsData(ctx, instrumentedSlidingWindow, metricsSystem, logger)

	// Example 3: Configuration Management
	fmt.Println("\n⚙️  Example 3: Dynamic Configuration")
	fmt.Println("===================================")

	// Note: In a real application, you would connect to an actual database
	// For this example, we'll show the interface
	configManager := setupConfigurationDemo(redisClient, logger)
	defer configManager.Stop()

	// Example 4: Health Checking
	fmt.Println("\n🏥 Example 4: Health Monitoring")
	fmt.Println("===============================")

	healthChecker := metrics.NewHealthChecker(metricsSystem, logger)

	// Register health checks
	healthChecker.RegisterDependency("redis", func(ctx context.Context) error {
		return redisClient.Ping(ctx).Err()
	})

	healthChecker.RegisterDependency("rate_limiter", func(ctx context.Context) error {
		return limiter.HealthCheck(ctx)
	})

	// Perform health check
	healthStatus := healthChecker.Check(ctx)

	fmt.Printf("Overall Health: %v\n", healthStatus.Healthy)
	for name, status := range healthStatus.Dependencies {
		healthIcon := "v"
		if !status.Healthy {
			healthIcon = "x"
		}
		fmt.Printf("%s %s: %s (latency: %s)\n", healthIcon, name,
			func() string {
				if status.Healthy {
					return "healthy"
				}
				return "unhealthy"
			}(), status.Latency)
	}

	// Example 5: Batch Operations
	fmt.Println("\n🔄 Example 5: Batch Operations")
	fmt.Println("==============================")

	testBatchOperations(ctx, limiter, logger)

	// Example 6: Leaky Bucket Algorithm
	fmt.Println("\n🪣 Example 6: Leaky Bucket Algorithm")
	fmt.Println("===================================")

	testLeakyBucket(ctx, limiter, logger)

	fmt.Println("\n All advanced examples completed!")
	fmt.Println(" Metrics available at: http://localhost:2112/metrics")
	fmt.Println("Press Ctrl+C to exit...")

	// Keep the server running
	select {}
}

func testAlgorithms(ctx context.Context, limiter *ratelimiter.RateLimiter, logger *zap.Logger) {
	// Test sliding window
	fmt.Println("\nTesting Sliding Window Algorithm:")

	slidingOptions := ratelimiter.RateLimitOptions{
		UserID:    "sliding_user",
		Limit:     3,
		Algorithm: ratelimiter.AlgorithmSlidingWindow,
		Window:    time.Second,
	}

	for i := 1; i <= 5; i++ {
		allowed, err := limiter.RateLimitWithOptions(ctx, slidingOptions)
		if err != nil {
			logger.Error("Rate limit error", zap.Error(err))
			continue
		}

		status := "v ALLOWED"
		if !allowed {
			status = "x DENIED"
		}
		fmt.Printf("Request %d: %s\n", i, status)
	}

	// Test leaky bucket
	fmt.Println("\nTesting Leaky Bucket Algorithm:")

	leakyOptions := ratelimiter.RateLimitOptions{
		UserID:    "leaky_user",
		Algorithm: ratelimiter.AlgorithmLeakyBucket,
		Capacity:  3,
		LeakRate:  1.0, // 1 token per second
	}

	for i := 1; i <= 5; i++ {
		allowed, err := limiter.RateLimitWithOptions(ctx, leakyOptions)
		if err != nil {
			logger.Error("Rate limit error", zap.Error(err))
			continue
		}

		status := "v ALLOWED"
		if !allowed {
			status = "x DENIED"
		}
		fmt.Printf("Request %d: %s\n", i, status)

		// Small delay to show leaking behavior
		if i == 3 {
			fmt.Println("Waiting 2 seconds for bucket to leak...")
			time.Sleep(2 * time.Second)
		}
	}
}

func generateMetricsData(ctx context.Context, rateLimitFunc func(context.Context, string, int) (bool, error), metricsSystem *metrics.Metrics, logger *zap.Logger) {
	fmt.Println("Generating metrics data...")

	users := []string{"user1", "user2", "user3"}

	for _, user := range users {
		for i := 0; i < 10; i++ {
			allowed, err := rateLimitFunc(ctx, user, 5)

			if err != nil {
				logger.Error("Rate limit error",
					zap.String("user", user),
					zap.Error(err))
			} else if !allowed {
				logger.Info("Rate limit exceeded",
					zap.String("user", user))
			}

			time.Sleep(50 * time.Millisecond)

			// Simulate some errors for metrics
			if i == 7 {
				metricsSystem.RecordConfigError()
			}
		}
	}

	// Update connection count (simulate)
	metricsSystem.UpdateRedisConnections(45)

	fmt.Println("v Metrics data generated")
}

func setupConfigurationDemo(redisClient *redis.Client, logger *zap.Logger) *services.Manager {
	fmt.Println("Setting up configuration management...")

	// In a real application, you would use an actual database connection
	// For demo purposes, we'll use nil and handle it gracefully
	var db *sql.DB = nil

	userRepo := repository.NewUserRepository(config.SQLDatabase{DB: db})
	configManager := services.NewManager(redisClient, logger, userRepo)

	// Set some default limits
	configManager.SetDefaultLimit("basic", 100)
	configManager.SetDefaultLimit("premium", 1000)
	configManager.SetDefaultLimit("enterprise", 10000)

	// Start the configuration manager (would normally start background processes)
	// For demo, we'll skip the actual start since we don't have a real DB

	// Simulate setting a user limit
	ctx := context.Background()
	if err := configManager.UpdateUserLimit(ctx, "demo_user", 500, time.Hour); err != nil {
		logger.Error("Failed to update user limit", zap.Error(err))
	} else {
		fmt.Println("v Updated limit for demo_user to 500 req/sec")
	}

	return configManager
}

func testBatchOperations(ctx context.Context, limiter *ratelimiter.RateLimiter, logger *zap.Logger) {
	slidingWindow := ratelimiter.NewSlidingWindow(redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	}), 1)

	// Create batch requests
	requests := []ratelimiter.RateLimitRequest{
		{UserID: "batch_user_1", Limit: 3, Window: time.Second},
		{UserID: "batch_user_2", Limit: 3, Window: time.Second},
		{UserID: "batch_user_3", Limit: 3, Window: time.Second},
		{UserID: "batch_user_1", Limit: 3, Window: time.Second}, // Same user again
	}

	responses, err := slidingWindow.RateLimitBatch(ctx, requests)
	if err != nil {
		logger.Error("Batch operation failed", zap.Error(err))
		return
	}

	fmt.Printf("Batch processed %d requests:\n", len(responses))
	for i, resp := range responses {
		if resp.Error != nil {
			fmt.Printf("Request %d (%s): ERROR - %v\n", i+1, resp.UserID, resp.Error)
		} else {
			status := "v ALLOWED"
			if !resp.Allowed {
				status = "x DENIED"
			}
			fmt.Printf("Request %d (%s): %s\n", i+1, resp.UserID, status)
		}
	}
}

func testLeakyBucket(ctx context.Context, limiter *ratelimiter.RateLimiter, logger *zap.Logger) {
	leakyBucket := ratelimiter.NewLeakyBucket(redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	}))

	userId := "leaky_demo_user"
	capacity := 5
	leakRate := 2.0 // 2 tokens per second

	fmt.Printf("Testing leaky bucket: capacity=%d, leak_rate=%.1f tokens/sec\n", capacity, leakRate)

	// Fill bucket quickly
	for i := 1; i <= 7; i++ {
		result, err := leakyBucket.RateLimitWithDetails(ctx, userId, capacity, leakRate, 1)
		if err != nil {
			logger.Error("Leaky bucket error", zap.Error(err))
			continue
		}

		status := "v ALLOWED"
		if !result.Allowed {
			status = "x DENIED"
		}

		fmt.Printf("Request %d: %s (remaining: %.1f tokens", i, status, result.RemainingTokens)
		if !result.Allowed && result.WaitTime > 0 {
			fmt.Printf(", wait: %v", result.WaitTime)
		}
		fmt.Println(")")

		// Add delay to show leaking
		if i == 5 {
			fmt.Println("Waiting 1 second for leaking...")
			time.Sleep(time.Second)
		}
	}

	// Show bucket state
	state, err := leakyBucket.GetBucketState(ctx, userId, capacity, leakRate)
	if err != nil {
		logger.Error("Failed to get bucket state", zap.Error(err))
	} else {
		fmt.Printf("Final bucket state: %.1f tokens, capacity: %d, leak rate: %.1f/sec\n",
			state.Tokens, state.Capacity, state.LeakRate)
	}
}
