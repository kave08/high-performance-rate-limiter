# High-Performance Rate Limiter Makefile

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
BINARY_NAME=rate-limiter
DOCKER_IMAGE=rate-limiter

# Build info
VERSION?=$(shell git describe --tags --always --dirty)
BUILD_TIME=$(shell date +%FT%T%z)
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

.PHONY: all build clean test test-coverage test-integration benchmark deps docker docker-build docker-run docker-stop help

# Default target
all: test build

# Build the application
build:
	$(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) ./examples/http_middleware/
	@echo "✅ Build completed: $(BINARY_NAME)"

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	docker-compose down -v 2>/dev/null || true
	@echo "✅ Cleaned build artifacts"

# Run tests
test:
	$(GOTEST) -v ./...
	@echo "✅ Unit tests completed"

# Run tests with coverage
test-coverage:
	$(GOTEST) -v -race -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report generated: coverage.html"

# Run integration tests (requires Docker)
test-integration:
	@echo "🐳 Starting test containers..."
	docker-compose -f docker-compose.test.yml up -d redis postgres
	@echo "⏳ Waiting for services to be ready..."
	sleep 10
	$(GOTEST) -v ./test/...
	docker-compose -f docker-compose.test.yml down
	@echo "✅ Integration tests completed"

# Run benchmarks
benchmark:
	@echo "📊 Running benchmarks..."
	$(GOTEST) -bench=. -benchmem -benchtime=10s ./test/
	@echo "✅ Benchmarks completed"

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy
	@echo "✅ Dependencies updated"

# Verify dependencies
verify:
	$(GOMOD) verify
	@echo "✅ Dependencies verified"

# Run linter
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run
	@echo "✅ Linting completed"

# Format code
fmt:
	$(GOCMD) fmt ./...
	@echo "✅ Code formatted"

# Security scan
security:
	@which gosec > /dev/null || (echo "Installing gosec..." && go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest)
	gosec ./...
	@echo "✅ Security scan completed"

# Build Docker image
docker-build:
	docker build -t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest .
	@echo "✅ Docker image built: $(DOCKER_IMAGE):$(VERSION)"

# Start full environment with Docker Compose
docker-up:
	docker-compose up -d
	@echo "🐳 Environment started"
	@echo "📖 Demo app: http://localhost:8080"
	@echo "📊 Grafana: http://localhost:3000 (admin/admin)"
	@echo "📈 Prometheus: http://localhost:9090"

# Stop Docker environment
docker-down:
	docker-compose down
	@echo "🐳 Environment stopped"

# View logs
logs:
	docker-compose logs -f

# Run load test
load-test:
	docker-compose --profile testing up --build load_tester
	@echo "💥 Load test completed"

# Development setup
dev-setup: deps
	@echo "🛠️  Setting up development environment..."
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@which gosec > /dev/null || go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest
	docker-compose up -d redis postgres
	@echo "✅ Development environment ready"
	@echo "🔗 Redis: localhost:6379"
	@echo "🐘 PostgreSQL: localhost:5432"

# Run example applications
run-basic:
	$(GOBUILD) -o examples/basic/basic examples/basic_usage/main.go
	./examples/basic/basic
	rm -f examples/basic/basic

run-advanced:
	$(GOBUILD) -o examples/advanced/advanced examples/advanced_usage/main.go
	./examples/advanced/advanced
	rm -f examples/advanced/advanced

run-http:
	$(GOBUILD) -o examples/http/http examples/http_middleware/main.go
	./examples/http/http
	rm -f examples/http/http

# Database migrations (if using migrations)
migrate-up:
	@which migrate > /dev/null || (echo "Please install golang-migrate: https://github.com/golang-migrate/migrate" && exit 1)
	migrate -path ./migrations -database "postgres://rate_limiter:rate_limiter_password@localhost:5432/rate_limiter?sslmode=disable" up

migrate-down:
	@which migrate > /dev/null || (echo "Please install golang-migrate: https://github.com/golang-migrate/migrate" && exit 1)
	migrate -path ./migrations -database "postgres://rate_limiter:rate_limiter_password@localhost:5432/rate_limiter?sslmode=disable" down

# Performance profiling
profile-cpu:
	$(GOTEST) -cpuprofile=cpu.prof -bench=. ./test/
	$(GOCMD) tool pprof cpu.prof

profile-mem:
	$(GOTEST) -memprofile=mem.prof -bench=. ./test/
	$(GOCMD) tool pprof mem.prof

# Generate documentation
docs:
	@which godoc > /dev/null || go install golang.org/x/tools/cmd/godoc@latest
	@echo "📚 Starting documentation server..."
	@echo "🌐 Documentation available at: http://localhost:6060/pkg/github.com/kave/high-performance-rate-limiter/"
	godoc -http=:6060

# Release (tag and build)
release: test lint security
	@read -p "Enter version (e.g., v1.0.0): " VERSION; \
	git tag -a $$VERSION -m "Release $$VERSION"; \
	git push origin $$VERSION; \
	$(MAKE) docker-build VERSION=$$VERSION
	@echo "🚀 Release $$VERSION created"

# CI/CD helpers
ci-test: deps test test-integration benchmark lint security
	@echo "✅ CI tests passed"

ci-build: ci-test build docker-build
	@echo "✅ CI build completed"

# Help target
help:
	@echo "High-Performance Rate Limiter - Available targets:"
	@echo ""
	@echo "Building:"
	@echo "  build          Build the application"
	@echo "  clean          Clean build artifacts"
	@echo ""
	@echo "Testing:"
	@echo "  test           Run unit tests"
	@echo "  test-coverage  Run tests with coverage report"
	@echo "  test-integration  Run integration tests (requires Docker)"
	@echo "  benchmark      Run performance benchmarks"
	@echo ""
	@echo "Development:"
	@echo "  deps           Download and tidy dependencies"
	@echo "  fmt            Format code"
	@echo "  lint           Run linter"
	@echo "  security       Run security scan"
	@echo "  dev-setup      Setup development environment"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build   Build Docker image"
	@echo "  docker-up      Start full environment"
	@echo "  docker-down    Stop Docker environment"
	@echo "  logs           View Docker logs"
	@echo ""
	@echo "Examples:"
	@echo "  run-basic      Run basic usage example"
	@echo "  run-advanced   Run advanced usage example"
	@echo "  run-http       Run HTTP middleware example"
	@echo ""
	@echo "Performance:"
	@echo "  load-test      Run load test"
	@echo "  profile-cpu    Run CPU profiling"
	@echo "  profile-mem    Run memory profiling"
	@echo ""
	@echo "Documentation:"
	@echo "  docs           Start documentation server"
	@echo ""
	@echo "Release:"
	@echo "  release        Create a new release"
	@echo "  ci-test        Run CI test suite"
	@echo "  ci-build       Run CI build pipeline"