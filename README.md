# FizzBuzz API

An HTTP service for generalized FizzBuzz. It returns the numbers from 1 to `limit`. Multiples of `int1` become `str1`, multiples of `int2` become `str2`, and multiples of both become `str1str2`. A statistics endpoint shows the most frequent request.

A live instance runs at <https://fizzbuzz.khalizov.com/>, with 2 to 20 replicas on a k3s cluster and statistics in Redis. It exposes the page, `/fizzbuzz` and `/stats`. The CDN blocks `/healthz` and `/metrics`, which are for the cluster only. Open it in a browser for the page, or send a request:

```sh
curl 'https://fizzbuzz.khalizov.com/fizzbuzz?int1=3&int2=5&limit=15&str1=fizz&str2=buzz'
curl https://fizzbuzz.khalizov.com/stats
```

## What "production ready" means here

The brief asks for a service that is "ready for production" and "easy to maintain by other developers". It does not say who runs the service, how much traffic it gets, or where it runs. These answers change the design. A prototype at a start-up and an authorization service at a cloud provider both run in production, but they need very different work. Before I started, I asked if I should clarify these points. The answer was to make my own assumptions and document them in this README.

I assumed this context:

- A product team owns the service. It runs on a shared container platform, such as Kubernetes or ECS.
- The platform supplies TLS, a gateway with rate limits, log collection from stdout and a Prometheus-compatible scraper.
- The API is public and read-only. It has no user accounts. The service keeps user input only in the statistics.
- The traffic and the values of `limit` are not known. Thus one request must not use a large part of the memory, the CPU or the uplink.
- The statistics are a product feature, not billing data. A count lost in a crash is acceptable. A wrong winner is not.

In this context, the two requirements mean this:

| Requirement | Meaning here |
|---|---|
| Ready for production | Each request has a bounded cost. Each error names its cause. Shutdown waits for open requests. Logs and metrics show what the service does. CI tests each change and builds the image. |
| Easy to maintain | The binary has three direct dependencies. Each concern has one place in the code. Each request limit has a written reason. Tests guard the facts that the design depends on. [Maintain](#maintain) lists them. |

A large `limit` is the main risk to memory and bandwidth, so I put most of the effort into performance. The generator streams the response with constant memory, and its main loop has no division. This is also my preference: I like performance work, and I think I add the most there. I did not add layers of enterprise abstraction.

[Decisions that depend on the context](#decisions-that-depend-on-the-context) shows what changes in other contexts, and the condition for each change. [Assumptions](#assumptions) gives the rules of the API.

## Run

With Docker:

```sh
docker build -t fizzbuzz .
docker run --rm -p 8080:8080 fizzbuzz
```

With Docker Compose and the Redis statistics store, as with several replicas:

```sh
docker compose up --build
```

With Go 1.27 or later:

```sh
go run .
```

Then send a request:

```sh
curl 'localhost:8080/fizzbuzz?int1=3&int2=5&limit=15&str1=fizz&str2=buzz'
curl localhost:8080/stats
```

You can also open <http://localhost:8080/>. The page sends real requests and draws each element of the response as one pixel. It also checks each element against the rule.

Tests, lint and benchmarks:

```sh
go test -race ./...
golangci-lint run
go test -run='^$' -bench=. -benchmem ./...
```

`make check` runs the same lint and tests as CI. `make help` shows the other targets.

`loadtest/` has the [k6](https://k6.io/) scripts that tested the live instance. Each script tells how to run it.

## Configuration

All settings come from environment variables. If a value is not valid, the server does not start and lists all problems.

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `MAX_LIMIT` | `10000000` | Largest `limit`. Must be less than 2³² |
| `MAX_STR_BYTES` | `1024` | Longest `str1` or `str2`, in bytes |
| `MAX_RESPONSE_BYTES` | `268435456` | Largest response (256 MiB) |
| `MAX_INFLIGHT_BYTES` | `1073741824` | Response bytes in transfer at the same time before `503` (1 GiB). Must be at least `MAX_RESPONSE_BYTES` |
| `STATS_STORE` | `memory` | `memory` (one count for each process) or `redis` (one count for all replicas) |
| `STATS_MAX_BYTES` | `67108864` | Memory budget for statistics (64 MiB). Must be less than 608 GiB |
| `STATS_FLUSH_INTERVAL` | `100ms` | With the Redis store, the time between two batches of counts |
| `REDIS_ADDR` | | `host:port`. Necessary when `STATS_STORE=redis` |
| `REDIS_TIMEOUT` | `100ms` | Timeout for each statistics call |
| `READ_HEADER_TIMEOUT`, `READ_TIMEOUT`, `IDLE_TIMEOUT` | `5s`, `10s`, `120s` | Connection timeouts |
| `WRITE_CHUNK_TIMEOUT` | `10s` | Deadline for each 32 KiB write of a response |
| `SHUTDOWN_TIMEOUT` | `25s` | Time for open requests to finish after SIGTERM |
| `MAX_HEADER_BYTES` | `16384` | Largest request line and headers |

### Why these limits

Each limit has a reason. If the reason is a fact that can change, a test fails when it stops being true.

| Setting | Reason | Test |
|---|---|---|
| `MAX_LIMIT` | The largest power of ten at which the classic request (`3`, `5`, `fizz`, `buzz`) fits the response maximum. At 10,000,000 it is 88 MB. At 100,000,000 it is 934 MB. | `TestDefaultLimits` |
| `MAX_RESPONSE_BYTES` | A JavaScript client that calls `response.json()` or `response.text()` holds the body as one string. V8 limits a string to 536,870,888 characters (512 MiB − 24). 256 MiB is half of 512 MiB. | `TestDefaultLimits` |
| `MAX_STR_BYTES`, `MAX_HEADER_BYTES` | Cloudflare limits a URL to 16 KB. Percent encoding makes one byte into 3 bytes or less. The largest request has two 1 KiB strings and the largest integers. It is 6,278 bytes. | `TestHeaderFitsStrings` |
| `STATS_FLUSH_INTERVAL` | A choice. It is the maximum age of the counts from other replicas, and the maximum loss when a replica stops without a normal shutdown. With 20 replicas, Redis gets 200 batches per second or less. | None |
| `MAX_INFLIGHT_BYTES` | A choice: four responses of the maximum size at the same time. The memory for each response is constant, so this limit protects the uplink. The uplink of the live instance sends 48 MB/s. At that rate, 1 GiB takes 22 s. | None |

The size of a response depends on `limit` and on the two strings. Thus a request can fail when each parameter is in range. The defaults give this guarantee: every request fits if `str1` and `str2` together have 23 bytes or less after JSON escaping. The worst case is `int1=int2=1`, where each element is `"str1str2",`. With 23 bytes, the response is 10,000,000 × 26 + 1 = 260,000,001 bytes. With 24 bytes, it is 270,000,001 bytes, which is more than 256 MiB.

For longer strings, the error gives the largest `limit` that fits. The server finds this value with a binary search. The search uses the same size formula as `Content-Length`, so the value is exact. `FuzzSizeLimit` checks the guarantee and the suggested limit with random parameters.

## API

### GET /fizzbuzz

| Parameter | Rule |
|---|---|
| `int1`, `int2` | Integer from 1 to 2⁶³−1 |
| `limit` | Integer from 1 to 10,000,000 |
| `str1`, `str2` | Not empty, valid UTF-8, 1,024 bytes or less |

```sh
$ curl 'localhost:8080/fizzbuzz?int1=3&int2=5&limit=15&str1=fizz&str2=buzz'
["1","2","fizz","4","buzz","fizz","7","8","fizz","buzz","11","fizz","13","14","fizzbuzz"]
```

The response has an exact `Content-Length`. A proxy that compresses the response removes this header. Cloudflare does this for the live instance. `HEAD` gives the same headers without the body.

If a request is not valid, the server returns `400` and names the parameter:

```sh
$ curl 'localhost:8080/fizzbuzz?int1=3&int2=5&limit=0&str1=fizz&str2=buzz'
{"error":"limit: must be between 1 and 10000000"}
```

The server also returns `400` if the response would be larger than 256 MiB. This can occur when all parameters are in range, for example with long strings. The error gives the largest `limit` that fits with the given strings. See [Why these limits](#why-these-limits). If more than 1 GiB of responses is in transfer, the server returns `503` with `Retry-After: 1`.

An unknown path returns `404`. Other methods return `405` with an `Allow` header. Both have a JSON `error` body.

### QUERY /fizzbuzz

QUERY ([RFC 10008](https://www.rfc-editor.org/rfc/rfc10008)) sends the same parameters in the body, not in the URL. Like `GET`, it is safe and idempotent. The body uses the query-string encoding, so the rules and errors are the same. `/stats` counts a QUERY request and its `GET` equivalent as one request.

```sh
$ curl -X QUERY localhost:8080/fizzbuzz -H 'Content-Type: application/x-www-form-urlencoded' \
    --data 'int1=3&int2=5&limit=15&str1=fizz&str2=buzz'
["1","2","fizz","4","buzz","fizz","7","8","fizz","buzz","11","fizz","13","14","fizzbuzz"]
```

- A different or missing `Content-Type` returns `415`.
- A body larger than `MAX_HEADER_BYTES` returns `413`.
- The server ignores a query string on the URL.
- Each `/fizzbuzz` response has the header `Accept-Query: application/x-www-form-urlencoded`.

### GET /stats

Returns the most frequent valid `/fizzbuzz` request and its number of hits:

```sh
$ curl localhost:8080/stats
{"params":{"int1":3,"int2":5,"limit":15,"str1":"fizz","str2":"buzz"},"hits":42}
```

Before the first valid request, it returns `{"params":null,"hits":0}`. When the statistics budget is full, it returns `503`. See [Statistics](#statistics).

### GET /healthz

Returns `{"status":"ok"}`.

### GET /metrics

Returns Prometheus metrics:

- `http_requests_total{route,method,status}`
- `http_request_duration_seconds{route,method}`
- The standard Go runtime and process metrics

## Assumptions

The brief leaves these points open. This section gives the decision for each one.

### Requests and validation

- The endpoint uses `GET` with query parameters because the operation only reads. The count for `/stats` is a side effect for analytics.
- The response is a JSON array of strings, not the comma-separated text of the example in the brief. `str1` and `str2` can contain a comma, so a joined text would be ambiguous. Numbers are also strings (`"1"`).
- Each of the five parameters must occur one time. A missing or repeated parameter returns `400`. The server ignores unknown parameters.
- The server rejects `limit=0` and empty strings. It does not return an empty result for them.
- The string limit counts bytes, not characters. `é` is 2 bytes.
- An integer can have a leading `+` or leading zeros (`03`). In a query string, `+` is a space, so send `+3` as `%2B3`. For the same reason, `str1=a+b` becomes `"a b"`.
- The server checks syntax first, then ranges. In each check, the error names the first parameter that fails, in the order `int1`, `int2`, `limit`, `str1`, `str2`.

### Output rule

- "Multiples of both" means divisible by both numbers. It does not mean divisible by their product. With `int1=4` and `int2=6`, `str1str2` is at 12.
- `str1` is always before `str2`. Thus, if you swap `int1` and `int2`, the output changes.
- `int1` and `int2` can be equal. Then each multiple becomes `str1str2`.

## Statistics

`/stats` counts a valid `GET` or `QUERY /fizzbuzz` request that this process received and served. It does not count these requests:

- Requests that are not valid, and requests rejected with `503`
- `HEAD` requests
- `/stats`, `/healthz`, `/metrics` and the files of the page

Each load of the page sends one `/fizzbuzz` request, and that request counts.

Two requests are the same if their parsed parameters are the same. The order and the spelling of the parameters are not important, so `int1=03` equals `int1=3`. `(3, 5, fizz, buzz)` and `(5, 3, buzz, fizz)` are different requests because their output is different.

If more than one request has the top count, `/stats` returns one of them. The API does not specify which one.

By default, each process keeps its own count in memory. With `STATS_STORE=redis`, all replicas share one exact count in a Redis sorted set. A request does not wait for Redis. Each replica counts in its memory and sends its counts to Redis in one batch every `STATS_FLUSH_INTERVAL`. One atomic Lua script applies each batch. Each batch has a sequence number, so a retry after a lost reply does not count the batch again.

`/stats` sends the counts of its replica before it reads Redis. Thus a client sees its own requests. The counts from other replicas can be up to `STATS_FLUSH_INTERVAL` old. At a normal shutdown, the server sends its last counts.

If Redis is not available, `/fizzbuzz` continues to operate and `/stats` returns `503`. The replica keeps its counts and sends them when Redis is available again. The server logs the Redis error one time in 10 s or less often.

The counts are exact and use `STATS_MAX_BYTES` or less. [Limitations](#limitations) gives two rare cases in which the Redis store loses or repeats counts. When the budget is full, `/stats` returns `503`, because an exact winner is no longer known. The memory store stays in this state until restart. The Redis store stays in this state until you delete its keys. `/fizzbuzz` continues to operate, and the server logs one warning. With Redis, the counts that wait in a replica use the same budget. If they fill it, the same requests would also fill the budget in Redis, so the store saturates.

The memory store keeps each different set of `int1`, `int2`, `str1` and `str2` one time. Requests that differ only in `limit` share it. The budget charges 48 bytes for each different request. For each new set, it also charges 152 bytes and the heap size of the two strings. These numbers are the heap bytes of the Go maps at their lowest load. `TestCounterBudget` fails if the real memory is larger. The store keeps its own copy of the strings, because a parsed string can keep the whole query string or body in memory. The default budget holds 1.4 million requests that differ only in `limit`, 310,000 requests with new short strings, or 29,800 requests with new 1 KiB strings.

`int1` and `int2` are JSON numbers up to 2⁶³−1. JavaScript clients lose precision above 2⁵³.

## Performance

The generator does not divide, and it does not convert numbers to text one at a time. It keeps the next multiple of each divisor. It copies the plain numbers between two multiples from a decimal counter that it increments in place. The output goes through a 32 KiB buffer from a pool. Before it starts, the server calculates the exact response size in O(log limit). That size gives the `Content-Length` header and the size check. The package documentation has diagrams of the algorithm: run `go doc -all ./internal/fizzbuzz`.

Measurements on an Apple M1, with `int1=3` and `int2=5`, on one core:

| limit | Response | Generation | Allocated |
|---|---|---|---|
| 100 | 0.6 KB | 0.6 µs | 256 B |
| 1,000,000 | 8.3 MB | 4.6 ms (1.8 GB/s) | 260 B |
| 10,000,000 | 88 MB | 46 ms (1.9 GB/s) | About 300 B |

The memory for each request is constant for all limits. On the same machine, a `[]string` with `json.Marshal` uses about 36 ms and 53 MB for one million elements. PGO and `GOARM64=v8.4` gave no net gain in benchmarks, so the build does not use them.

With the Redis store, a request does not wait for Redis. On a 4-core Intel Xeon with Redis on the same machine, a request with `limit=100` takes 5.4 µs. A design with one script call for each request took 108 µs, and Redis used 5 µs of CPU for each hit. With batches, the load on Redis depends on the number of replicas and different requests, not on the traffic.

## Decisions that depend on the context

The first table shows how the design changes in four typical contexts. The tables after it give each decision: the choice in this repository for the [assumed context](#what-production-ready-means-here), and the condition that makes another choice better.

### Four contexts

| Context | What changes |
|---|---|
| Prototype at a start-up | The chart, the Redis store and the multi-arch image are not necessary. Keep the limits, the tests and the logs. One instance with the memory store is sufficient. |
| Product team on a shared platform (the assumed context) | Where the platform has its own chart, log fields, metrics stack or API guidelines, use them in place of the choices below. |
| B2B SaaS | Add authentication, a quota and statistics for each client, an OpenAPI file with versions, audit logs and a data retention policy. |
| Critical infrastructure, such as an authorization service | Add SLOs with alerts, canary releases, replicas in several zones or regions, and load tests in CI. `/fizzbuzz` already makes no network call, and too many large responses in transfer already get `503`. |

### Code

| Decision | This repository | Choose differently when |
|---|---|---|
| Layers | Two packages: HTTP in `main` and the rule in `internal/fizzbuzz`. `statsStore` is the only interface, because it has two implementations. | A second transport, such as gRPC or a Lambda handler, or more business rules appear. Then add a service layer between the handlers and the rule. Before that, a layer would only pass each call on. |
| Dependencies | gin, because I use it every day. The Prometheus client and go-redis. The standard library for logs (`log/slog`), configuration and tests. `main.go` connects the parts by hand. | The team has a standard stack, for example zap, viper, testify or fx. `net/http` can also replace gin: its router matches methods, `QUERY` included, and sends `405` with `Allow`. |
| Generator | Optimized for speed. It streams each response with constant memory. The plain loop `reference` in the tests is the specification, and fuzz tests compare the two. | No client needs large responses, and simple code is more important than speed. Then build the response with the plain loop and `json.Marshal`, and lower `MAX_LIMIT`. The endpoints and the response format do not change. |
| Tests | Unit tests, fuzz tests, store contract tests and benchmarks. The Redis tests use miniredis, a fake Redis in the test process. The k6 scripts run by hand against a deployed instance. | The Lua script grows, or the service has a latency SLO. Then also test against a real Redis, and run a load test in CI. |

### API contract

| Decision | This repository | Choose differently when |
|---|---|---|
| Specification | This README. There is no OpenAPI file. | Other teams generate clients, or the API guidelines require a specification file. |
| Versions | No version in the path. A breaking change would add `/v2/fizzbuzz` and keep `/fizzbuzz`. | The API guidelines require a prefix, such as `/v1`, from the start. |
| Error body | `{"error":"<param>: <reason>"}`. A `400` names the first parameter that fails. | The API guidelines require RFC 9457 problem details, or clients must see all errors at one time. |
| HTTP caching | No cache headers. `/stats` must see each request, so a CDN must not keep `/fizzbuzz` responses. | The statistics can come from the CDN logs. The output never changes for the same parameters, so the CDN can then keep it for a long time. |

### Statistics design

| Decision | This repository | Choose differently when |
|---|---|---|
| Location | In each process, or in Redis for all replicas. See [Statistics](#statistics). | A data platform already collects request logs. Then the service logs the parsed parameters, and a query in the platform answers `/stats`. The service keeps no state, but the logs then contain user input. |
| Accuracy | Exact counts in a memory budget. When the budget is full, `/stats` returns `503`. It never shows a wrong winner. | The number of different requests has no bound, and an estimate is acceptable. Then use an approximate top-k algorithm, such as Space-Saving, in fixed memory. |
| Time window | All requests since the process started (memory store) or since the keys were created (Redis store). | "Most frequent" must show current use. Then count in a window, for example the last 24 hours. |

### Operations

| Decision | This repository | Choose differently when |
|---|---|---|
| Platform | A container image and a Helm chart with an autoscaler, a PodDisruptionBudget and a spread over nodes. | The platform has its own chart or its own runtime. The image runs as it is on ECS. AWS Lambda needs an adapter and the Redis store. |
| Logs and traces | JSON logs without query strings. No request ID and no traces. | The platform follows requests across services. Then add a request ID and OpenTelemetry. A trace of `/fizzbuzz` would have one span, because it calls no other service. |
| SLOs and alerts | None. `http_requests_total` gives the error rate. `http_request_duration_seconds` includes the transfer of the body, so for a large response it measures the download speed of the client. | A team is on call for the service. Then set SLOs and alerts with that team. A latency SLO needs a separate measure for small responses, or the time to the first byte. |
| Release | Each push to `main` publishes an image tagged with the commit SHA and `latest`, with provenance and an SBOM. | The platform uses version tags, signed images or GitOps promotion between environments. |

### Security

| Decision | This repository | Choose differently when |
|---|---|---|
| Authentication and rate limits | No authentication: the API is public. The gateway applies the rate limits. | The API must know its clients, or there is no gateway. Then add API keys or OAuth 2.0, and a quota for each client. |
| Browser clients | No CORS headers. In a browser, only the page on the same origin can read the API. | Web apps on other origins call the API. Then allow a list of origins. |
| Public statistics | Everybody can read `/stats`, and it shows the strings of the top request. A client that sends many requests can put any text there. | The strings can contain personal data or offensive text. Then put `/stats` behind authentication, or count for each client. |
| Redis connection | Plain TCP without TLS or a password. This suits a Redis that only the cluster network can reach. | Redis is a managed service outside that network. Then add settings for TLS and authentication. |

### Conventions

Go teams use different style guides. This code follows Effective Go and Go Code Review Comments. CI checks the format with `gofmt` and `goimports` and runs the linters in `.golangci.yml`. The tests use the standard `testing` package without an assertion library.

The code already follows these rules of the Uber Go Style Guide: `main` exits in one place, there are no `init` functions, each goroutine has an owner that stops it and waits for it, and marshaled structs have field tags. A team on the Uber guide would change these points:

- A `_` prefix on unexported global names, for example `_flushScript`
- Field names in each struct literal, for example `&FieldError{Param: "int1", Reason: "must be at least 1"}`
- Two import groups, not three
- A soft limit of 99 characters for each line
- A compile-time check for each interface implementation, for example `var _ statsStore = (*counter)(nil)`

These changes are mechanical. They do not change behavior.

## Limitations

- The memory store keeps one count for each process, and a restart resets it. Behind a load balancer, use the Redis store.
- When the statistics budget is full, `/stats` stays unavailable. For the memory store, this continues until restart. For Redis, it continues until you delete the keys.
- With the Redis store, a replica that stops without a normal shutdown loses the counts of its last `STATS_FLUSH_INTERVAL`. Nothing reports this loss.
- With the Redis store, Redis keeps the sequence number of a replica for 24 h. If a replica cannot reach Redis for longer than that after Redis applied a batch, the retry counts the batch again.
- The binary needs a 64-bit platform. The compiler checks this.

## Deployment notes

- The logs are JSON on stdout. There is one line for each request (method, path, status, duration), and lines for start, shutdown and errors. The logs do not contain query strings.
- The server disconnects slow clients. These are the limits:
  - Headers: 5 s
  - Full request: 10 s
  - Each 32 KiB write of the response: 10 s. There is no global write timeout, so a large response to a healthy client does not stop.
  - Idle keep-alive connections: 120 s
  - Request headers: 16 KiB or less
- On SIGTERM, the server stops new connections. Open requests have 25 s to finish. The orchestrator must stop traffic to the pod before SIGTERM, for example with a `preStop` delay.
- Use `/healthz` for liveness and readiness probes. A Redis outage does not stop `/fizzbuzz`, so the probes do not check Redis, and one endpoint is sufficient.
- Do not expose `/metrics` through the public ingress. It is for the metrics scraper.
- Put TLS, authentication and rate limits in the gateway.
- The container runs as a non-root user on a distroless base image.
- `chart/` contains a Helm chart. A HorizontalPodAutoscaler keeps 2 to 20 replicas and scales on CPU and memory use. The chart spreads the replicas on different nodes and has a PodDisruptionBudget and a 5 s `preStop` sleep. It sets a read-only root filesystem and a `GOMEMLIMIT` from the memory limit. The default store is `memory`. With more than one replica, set `STATS_STORE=redis` and `REDIS_ADDR` in `env`.
- On each push to `main`, CI publishes a multi-arch image (amd64 and arm64) to GHCR, with the commit SHA and `latest` as tags. In production, pin `image.digest`.

## Maintain

[AGENTS.md](AGENTS.md) has the map of the code and the rules for a change. The rules apply to people too. These tests guard the facts that the design depends on:

| Fact | Tests |
|---|---|
| The output is byte-identical to `json.Marshal` of the plain loop `reference`. | `TestGrid`, `TestStrings`, `TestLarge`, `FuzzEquivalence` |
| `JSONSize` is the exact length of the output. The `limit` that an error suggests is the largest that fits. | The same tests, and `FuzzSizeLimit` |
| Each statistics store counts exactly or returns `errStatsUnavailable`. It never gives a wrong winner. | `TestStoreContract` and `TestStoreSaturation`, with the memory store and with Redis through miniredis |
| A retry of a Redis batch does not count the batch again. | `TestRedisRetryCountsOnce` |
| The memory store uses no more heap than its budget charges. | `TestCounterBudget` |
| The defaults keep the reasons in [Why these limits](#why-these-limits). | `TestDefaultLimits`, `TestHeaderFitsStrings` |
| Metric labels have a fixed set of values. | `TestMetricLabels` |

After a change in `internal/fizzbuzz`, run `make fuzz`. Support each performance claim with `benchstat` output from `make bench`, which runs each benchmark 10 times.

## How this was built

AI agents helped to build this service. [AGENTS.md](AGENTS.md) tells how.
