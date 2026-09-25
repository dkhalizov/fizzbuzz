# AGENTS.md

This file has the rules for AI coding agents in this repository. It also records how agents helped to build the service.

## Map

| Path | Contents |
|---|---|
| `internal/fizzbuzz/` | The generator: validation, the exact size formula and the streamed writer. `doc.go` explains the algorithm. |
| `server.go` | gin routes, query parsing, the `/fizzbuzz` and `/stats` handlers, admission control |
| `query.go` | The QUERY method |
| `web.go`, `web/` | The embedded page |
| `config.go` | All settings from environment variables, with their defaults |
| `stats.go` | The `statsStore` interface and the memory store |
| `redis_store.go` | The Redis store, for replicas. One atomic Lua script records each hit. |
| `observe.go` | Request log and Prometheus metrics |
| `main.go` | Wiring, server timeouts and shutdown |
| `chart/` | Helm chart. The repository that deploys it supplies the cluster values. |

## Commands

```sh
make check      # the same checks as CI: vet, golangci-lint, helm lint, go test -race
go test -race ./...
golangci-lint run
helm lint chart
go test -run='^$' -fuzz=FuzzEquivalence -fuzztime=60s ./internal/fizzbuzz
go test -run='^$' -bench=. -benchmem -count=10 ./... | tee new.txt   # compare with benchstat
```

## Rules

- The README is the API contract. If you change behavior, change the README assumptions in the same commit.
- The output of `internal/fizzbuzz` must be byte-identical to `json.Marshal` of the literal `reference` in its tests. After a change there, run the fuzz target.
- Support each performance claim with `benchstat` output from 10 runs or more. Remove an optimization that does not measure faster. PGO and `GOARM64` failed this test and are not in the build.
- Keep `JSONSize` and the generator in agreement. The tests compare them.
- Put each new setting in `config.go` and in the README configuration table in the same commit.
- Each `statsStore` must pass `TestStoreContract` and `TestStoreSaturation`. These tests run against the memory store and against Redis through miniredis. A store counts exactly or returns `errStatsUnavailable`. It must not report a winner that can be wrong.
- Do not count pages or static files in `/stats`. Give their routes fixed templates.
- Keep metric labels bounded: use route templates and a fixed set of methods. Do not use raw paths or query values as labels.
- The dependencies are gin, the Prometheus client and go-redis. miniredis is for tests only. Add a dependency only with a reason that a reviewer accepts.
- A comment tells why. Do not write comments that repeat the code.
- Write text and comments in plain technical English: short sentences, active voice, one idea in each sentence.

## How agents helped

The take-home permits AI, and this file records all of it.

- Design: Claude Code (Claude Opus 5.5) wrote the first scope, the assumptions and the analysis of edge cases. OpenAI Codex (`gpt-5.6-sol`, read-only) did adversarial reviews of the design in several rounds. A new Claude Opus instance did a last independent check. Each disagreement had an explicit decision. For example, a statistics cap gave wrong answers without a signal, so an explicit 503 replaced it. Also, integer parsing depended on the platform, and a fix made it the same on all platforms.
- Performance: a separate investigation compared three generator designs with benchmarks and a research note. The service uses the fastest streamed design.
- Implementation: Claude Code wrote the code and tests. A second Claude Code session added the QUERY method and the page. A test or a measurement checked each claim about behavior, and some claims failed. For example, an agent suspected a bug with write deadlines on keep-alive connections. `net/http` already handles that case, so Claude Code removed the fix and its test.
- Text: agents edited all text to plain technical English (ASD-STE100) and removed the patterns that Wikipedia lists as signs of AI writing.
- Human decisions: the focus on performance, gin, the limits, and what to leave out.
