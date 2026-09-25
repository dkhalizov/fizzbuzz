.PHONY: help run test lint bench fuzz docker check

help: ## List targets
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

run: ## Start the server on :8080
	go run .

test: ## Unit tests with the race detector
	go test -race -count=1 ./...

lint: ## go vet, golangci-lint, helm lint
	go vet ./...
	golangci-lint run
	helm lint chart

bench: ## Benchmarks, 10 runs, for benchstat
	go test -run='^$$' -bench=. -benchmem -count=10 ./... | tee bench.txt

fuzz: ## Both fuzz targets, 60 s each
	go test -run='^$$' -fuzz=FuzzEquivalence -fuzztime=60s ./internal/fizzbuzz
	go test -run='^$$' -fuzz=FuzzSizeLimit -fuzztime=60s ./internal/fizzbuzz

docker: ## Build and run the image on :8080
	docker build -t fizzbuzz .
	docker run --rm -p 8080:8080 fizzbuzz

check: lint test ## Everything CI runs before the image build
