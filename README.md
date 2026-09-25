# FizzBuzz API

An HTTP service for generalized FizzBuzz. It returns the numbers from 1 to `limit`. Multiples of `int1` become `str1`, multiples of `int2` become `str2`, and multiples of both become `str1str2`. A statistics endpoint shows the most frequent request.

## A note on direction

The brief leaves scale, traffic and deployment open. I put the effort into performance. The generator streams the response with constant memory, and its main loop has no division. The benchmarks for that choice are in the repository. I did not add layers of enterprise abstraction or infrastructure. That is a personal preference: I like performance work, and I think I add the most there.

I use gin because it is what I work with every day. The behavior of the service does not depend on the router.

The traffic, the deployment target and the real limits are not known, so this is one small stateless service. It runs as it is in a container on ECS or Kubernetes. You can also wrap it for AWS Lambda behind API Gateway without a change to the core. On Lambda or with several replicas, `/stats` needs the Redis store.

## Run

With Docker:

```sh
docker build -t fizzbuzz .
docker run --rm -p 8080:8080 fizzbuzz
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

You can also open <http://localhost:8080/>. The page sends real requests and draws each element of the response as one pixel. It also checks the bytes against `Content-Length` and the rule.

Tests, lint and benchmarks:

```sh
go test -race ./...
golangci-lint run
go test -run='^$' -bench=. -benchmem ./...
```

`make check` runs the same lint and tests as CI. `make help` shows the other targets.

## Configuration

All settings come from environment variables. If a value is not valid, the server does not start and lists all problems.

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `MAX_LIMIT` | `10000000` | Largest `limit` |
| `MAX_STR_BYTES` | `1024` | Longest `str1` or `str2`, in bytes |
| `MAX_RESPONSE_BYTES` | `268435456` | Largest response (256 MiB) |
| `MAX_INFLIGHT_BYTES` | `1073741824` | Response bytes in transfer at the same time before `503` (1 GiB). Must be at least `MAX_RESPONSE_BYTES` |
| `STATS_STORE` | `memory` | `memory` (one count for each process) or `redis` (one count for all replicas) |
| `STATS_MAX_BYTES` | `67108864` | Memory budget for statistics (64 MiB) |
| `REDIS_ADDR` | | `host:port`. Necessary when `STATS_STORE=redis` |
| `REDIS_TIMEOUT` | `100ms` | Timeout for each statistics call |
| `READ_HEADER_TIMEOUT`, `READ_TIMEOUT`, `IDLE_TIMEOUT` | `5s`, `10s`, `120s` | Connection timeouts |
| `WRITE_CHUNK_TIMEOUT` | `10s` | Deadline for each 32 KiB write of a response |
| `SHUTDOWN_TIMEOUT` | `25s` | Time for open requests to finish after SIGTERM |
| `MAX_HEADER_BYTES` | `16384` | Largest request line and headers |

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

The response has an exact `Content-Length`. `HEAD` gives the same headers without the body.

If a request is not valid, the server returns `400` and names the parameter:

```sh
$ curl 'localhost:8080/fizzbuzz?int1=3&int2=5&limit=0&str1=fizz&str2=buzz'
{"error":"limit: must be between 1 and 10000000"}
```

The server also returns `400` if the response would be larger than 256 MiB. This can occur when all parameters are in range, for example with long strings that JSON must escape. If more than 1 GiB of responses is in transfer, the server returns `503` with `Retry-After: 1`.

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
- The response is a JSON array of strings. Numbers are also strings (`"1"`).
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

By default, each process keeps its own count in memory. With `STATS_STORE=redis`, all replicas share one exact count in a Redis sorted set. One atomic Lua script records each hit. If Redis is not available, `/fizzbuzz` continues to operate and `/stats` returns `503`. The server logs the Redis error one time in 10 s or less often.

The counts are exact and use `STATS_MAX_BYTES` or less. Each different request uses a part of the budget that depends on the size of its strings. When the budget is full, `/stats` returns `503`, because an exact winner is no longer known. The memory store stays in this state until restart. The Redis store stays in this state until you delete its keys. `/fizzbuzz` continues to operate, and the server logs one warning.

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

## Limitations

- The memory store keeps one count for each process, and a restart resets it. Behind a load balancer, use the Redis store.
- When the statistics budget is full, `/stats` stays unavailable. For the memory store, this continues until restart. For Redis, it continues until you delete the keys.
- With the Redis store, each `/fizzbuzz` request adds one round trip to Redis.
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
- Use `/healthz` for liveness and readiness probes. The service has no dependencies, so one endpoint is sufficient.
- Do not expose `/metrics` through the public ingress. It is for the metrics scraper.
- Put TLS, authentication and rate limits in the gateway.
- The container runs as a non-root user on a distroless base image.
- `chart/` contains a Helm chart. It has 2 replicas on different nodes, a PodDisruptionBudget and a 5 s `preStop` sleep. It also sets a read-only root filesystem and a `GOMEMLIMIT` from the memory limit.
- On each push to `main`, CI publishes a multi-arch image (amd64 and arm64) to GHCR.

## How this was built

AI agents helped to build this service. [AGENTS.md](AGENTS.md) tells how.
