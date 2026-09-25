# Performance limits of the fizzbuzz service

This is a research branch. It will not be merged. It measures how fast the
service can get and what each gain costs. Each claim has `benchstat` output from
10 runs or more, in the commit message of its change and in `research/results/`.

## Summary

| Path | Baseline | Final service | Change | Rover (no net/http) |
|---|---|---|---|---|
| limit=100, 2 server cores, RPS | 45.6k | 78.6k | +72% | 279k (1 run, wrk also saturated) |
| limit=100, 1 server core, RPS | 27.9k (2 runs) | 43.1k | | 116.2k (+170% against the final service) |
| limit=100, p99 at 64 connections | 6.05 ms | 3.12 ms | −48% | |
| limit=10,000,000, GB/s | 1.70 | 5.81 | +243% | 5.3 to 5.6 |
| limit=10,000,000, p50 latency | 206 ms | 59 ms | −71% | |
| Handler, limit=100 (BenchmarkServe) | 3.69 µs, 21 allocs | 1.46 µs, 0 allocs | −60% | |

Three changes give most of the gain:

1. The request log: a hand-written line in a batch buffer, not one slog write for
   each request.
2. Block templates in the generator: the large path is 7 to 11 times faster in
   the generator and 3.4 times faster over TCP.
3. Fewer allocations on the request path: one plan, the direct parser, the
   header values, GOGC=400 and PGO.

## Method

- Machine: 4-core Intel Xeon 2.1 GHz (Sapphire Rapids, KVM guest), 16 GB, Linux
  6.18, Go 1.27.0. The noise of a benchmark on this VM is ±3 to 9%.
- In-process: `BenchmarkServe` and `BenchmarkWriteJSON`. Old and new test
  binaries run alternately, 10 times each.
- Over TCP: `research/load/ab.sh` starts the old and the new server alternately,
  10 times each, 5 s of wrk after 2 s of warm-up. The server runs on cores 0-1
  with GOMAXPROCS=2 and wrk on cores 2-3. The output is in benchstat format:
  `sec/op` is server core time for each request.
- Log sink: stdout goes through a pipe to a reader on the server cores
  (`LOGSINK=pipe`), as in a container. Some Phase 1 numbers use /dev/null. The
  text says which.
- `perf stat` has no hardware counters in this guest (cycles, instructions,
  cache and branch misses are `<not supported>`). The profiles use `perf record
  -e cpu-clock` and `go tool pprof`.
- k6 is not installed. wrk with a Lua script gives RPS and p50/p99/p99.9.
- Correctness after each step: `go test -race ./...`, both fuzz targets for
  60 s after each generator change, and new fuzz targets for the parser and the
  log line against their references.

## Phase 1: baseline and ceilings

### Baseline

`BenchmarkServe` measured an error path that production does not take: the
test writer had no `SetWriteDeadline`, so each write built an `ErrNotSupported`
error with a stack trace. The first commit fixes the benchmark. All later
comparisons use the fixed benchmark.

| limit | sec/op | B/op | allocs/op |
|---|---|---|---|
| 100 | 3.81 µs | 756 | 21 |
| 10,000 | 55.9 µs | 749 | 21 |
| 1,000,000 | 5.08 ms | 964 | 21 |

Before the fix: 4.05 µs, 55.7 µs, 5.31 ms, and 527 allocations at 1,000,000.

Over TCP (2 server cores, stdout to /dev/null): 54.5k RPS, p50 1.1 ms,
p99 5.2 ms, p99.9 8 ms at 64 connections; 1.72 GB/s at limit=10,000,000 with 4
connections. With stdout to a pipe: 45.6k RPS and 1.70 GB/s.

### Ceilings

| Ceiling | How | Result |
|---|---|---|
| memcpy, 32 KiB (in cache) | `research/ceilings`, `copy` | 40.9 GiB/s |
| memcpy, 256 MiB (DRAM) | same | 16.5 GiB/s |
| Small response, raw Go sockets | goroutine per connection, fixed 614-byte body | 170k RPS (2 cores), 108k (1 core) |
| Small response, net/http floor | handler writes a fixed body | 95k RPS (2 cores), 54k (1 core) |
| Round trip, 1 connection | p50 | raw 37 µs, net/http 60 µs, service 64-71 µs |
| Large response, raw writes | fixed 32 KiB buffer, 88.9 MB body | 8.0 to 8.9 GB/s |
| Large response, sendfile | file in /dev/shm | 7.0 GB/s |
| Syscalls, small response | `strace -c` | 1 read and 1 write are the floor |
| Syscalls, large response | `strace -c` | 1 write for each 32 KiB: 2,713 for each response |

The baseline had 3.3 syscalls for each small response: a read, the response
write, a write of the log line, and 0.15 reads that returned EAGAIN. The final
service has 1.03 reads and 1.00 writes (`research/results/strace.small.final.txt`).

At the start, the service was at 57% of the net/http floor and 32% of raw
sockets for small responses. For large responses it was at 21% of the raw-write
ceiling and at 4% of memcpy.

## Results for each idea

Gain: in-process (`BenchmarkServe` unless noted) / over TCP. "n.s." means not
significant (p ≥ 0.05). The TCP gain is for RPS at limit=100 unless noted.

| # | Idea | Gain | Cost | Verdict |
|---|---|---|---|---|
| 1 | Hand-written JSON log line in a pooled buffer, byte-identical to slog (fuzzed) | −28.2% / +10.8% | 120 lines, a copy of the slog escaping rules | KEEP |
| 2 | Batched log: one write every 100 ms or 64 KiB | n.s. / +30.5% (pipe), +5.0% (/dev/null) | 70 lines. A crash loses the lines of the last 100 ms or less | KEEP |
| 3 | Sampled log: all errors, 1 in 100 others | n.s. / +4.9% | The log loses 99% of successful requests | TOO COSTLY |
| 4 | Metric series resolved one time, in a fixed array | −9.4% / n.s. | 50 lines, a route list to keep in sync (tested) | KEEP (in-process only) |
| 5 | One generator plan for each request, not three | −12.9% / +1.7% | 30 lines, one exported type | KEEP |
| 6 | Direct query parser, no url.Values (fuzzed against url.ParseQuery) | −11.2% / +2.5% | 60 lines. GODEBUG urlmaxqueryparams no longer applies | KEEP |
| 7 | Shared header values, not Header.Set | −8.0% / +1.7% | 10 lines. A change in place would affect all responses | KEEP |
| 8 | net/http sets Content-Length for bodies up to 2 KiB | −6.2% / n.s. | Depends on an unexported net/http constant (tested on the wire) | KEEP (in-process only) |
| 9 | Plan and generator state on the stack | −7.0% / +4.2% | 40 lines. Only allocs/op shows a regression | KEEP |
| 10 | Deadline writer without allocations (0 allocs/op) | −10.1% / n.s. | 30 lines | KEEP (in-process only) |
| 11 | Replace gin with ServeMux or a path switch | Routing only: gin 42 ns, ServeMux 74 ns, switch 3.7 ns. 39 ns is 0.13% of a request over TCP | A new router; gin is a human decision | TOO COSTLY |
| 12 | GOGC=400 (GOMEMLIMIT from the chart) | +5.5%, p99 −18% | 11 MB more RSS | KEEP (setting) |
| 13 | GOGC=off with GOMEMLIMIT=512MiB | +3.4% n.s. | RSS 496 MB | FAILED |
| 14 | GOAMD64=v3 | n.s. / n.s. | | FAILED |
| 15 | PGO with a profile from the load test | n.s. / +4.4% | 40 KB file, slower build, a profile to refresh | KEEP |
| 16 | Block templates for the large path | Generator at n=10M: 3_5 1.63 → 11.5 GiB/s, 2_3 1.43 → 16.5 GiB/s. TCP at limit=10M: 1.57 → 5.34 GB/s (+239%) | 200 lines. +5% at n=100 in the generator alone. 1 MiB pooled for each large response | KEEP |
| 17 | Blocks of about 1 MiB (m periods in one write) | TCP limit=10M: +2.3% responses/s; GB/s n.s. (p=0.063) | 3 lines | KEEP (marginal) |
| 18 | SO_SNDBUF 1 MiB and 4 MiB (rover) | No change (1 run each) | | FAILED |
| 19 | Write size 32 KiB, 64 KiB, 256 KiB, 1 MiB | Raw ceiling only: 8.8, 7.4, 7.6, 9.5 GB/s (3 runs each) | | Finding, used in 17 |
| 20 | Busy polling in rover (SPIN=1) | No gain, 2 runs | A core at 100% when idle | FAILED |
| 21 | Rover: raw epoll, SO_REUSEPORT, own HTTP/1.1 parser, response cache, sharded stats | 1 core: +170% (43.1k → 116.2k RPS), p99 −63% | 1,300 lines, own HTTP parser, no /metrics or page | TOO COSTLY for the product |
| 22 | Rover response cache for small responses | 198k → 279k RPS at 2 cores (1 run each) | 32 MB for each loop | Not verified with benchstat |
| 23 | Cache the most frequent large response | Calculated only (see Phase 3) | 88 MB, or 14.5 MB gzip | Not built |
| 24 | Per-P sharding of the stats counter in the service | Not tried: the profile shows no contention (`counter.Record` 0.7%) | | Not needed |
| 26 | Remove the request log (after 2) | −8.3% / +2.5% n.s. (p=0.052) | No record of single requests | FAILED by the rules; kept as the owner's decision |
| 25 | fasthttp or gnet on a sub-branch | Not done. Rover replaces net/http completely and shows the limit that these libraries can approach | | Not done |

## Phase 2: small responses

The profile at limit=100 over TCP (2 cores) at the start: 43% kernel, the
response write 34% (half of it the wakeup of the client), the gin handler 25%.
In the handler, slog was 26%, parsing 14% and the generator 31%.

The log was the largest cost. slog formats `duration_ms` with `encoding/json`
and writes one line with one syscall. With stdout as a pipe, each line also
wakes the reader. The hand-written line and the batch buffer together gave
about +40% RPS with a pipe (45.6k to 62.7k, from two separate A/B runs).

Allocations: the handler went from 21 to 0 for each request. The whole server
still makes 15 allocations for each request, all in net/http (header clone at
WriteHeader, request and header parsing, two `context.WithCancel`, the
background-read goroutine, the URL) and one for the first insert into the
response header map. `BenchmarkServeLoopback` measures this. 0 allocations for
the whole server needs a different HTTP stack.

Runtime: GOGC=400 and PGO both helped over TCP and not in-process. The earlier
PGO test in AGENTS.md probably used a benchmark profile. The gain is in net/http
and in the runtime, which `BenchmarkServe` does not run.

## Phase 3: large responses

The generator was at 1.5 GB/s for each core: 4% of memcpy. The block templates
use the fact that a block of L = lcm(int1, int2, 10^k) elements has fixed types
and fixed low digits. Only the high digits change, so the writer stores 1 to 4
bytes for each number into a prebuilt block and writes the block. At n=10M this
is 11.5 GiB/s for 3 and 5, and 16.5 GiB/s for 2 and 3. That is 28 to 40% of
memcpy in cache, and faster than the loopback network can take the bytes.

Over TCP the service now sends 5.8 GB/s: 65 to 72% of the raw-write ceiling
(8.0 to 8.9 GB/s). The profile of the server: 19% kernel copy, 17% hole stores,
15% wakeups, 9% idle. wrk uses its two cores fully. The rest of the gap to the
ceiling is the hole stores, which a raw server does not do.

Divisors without a short period (997 and 991, or 1,000,003 and 1,000,033) keep
the element loop at 1.3 to 1.5 GB/s.

Cache of the most frequent large response (calculated, not built): the classic
request at limit=10M is 88.1 MB. Generation costs 88 MB / 11.5 GiB/s = 7.1 ms
of CPU. A cache saves this CPU but not the kernel copy, so over loopback it can
gain at most the gap to the raw ceiling: about 50%. It costs 88 MB for each
entry. gzip -6 makes it 14.5 MB (16%) and costs 1.2 s of CPU one time, so it pays
back after 170 hits from clients that accept gzip. On the live instance the
uplink sends 48 MB/s, so the network, not the CPU, sets the time of a large
response: 1.8 s raw, 0.3 s with gzip. The precompressed body is the only
large-path change that the live instance would feel. The CDN compresses the
responses already, so it matters only between the origin and the CDN.

## Phase 4: concurrency and the kernel

- Scaling: the machine has 4 cores, and wrk needs 2 or 3 of them. The server can
  have only 1 or 2 cores with a client that is not the limit. The final service
  goes from 43k RPS on 1 core (wrk on 3 cores) to 79k on 2 cores (wrk on 2
  cores), 1.8 times. Rover on 2 cores
  saturates wrk too. A scaling curve above 2 cores needs a second machine.
- Stats contention: `counter.Record` is 0.7% of the server profile at 2 cores,
  and the lock (`runtime.lock2`) is 0.65%. Per-P sharding would gain less than
  the noise. Rover uses one shard for each loop, with an uncontended atomic add
  on a cache hit.
- SO_REUSEPORT with one listener for each loop is in rover. With keep-alive,
  accept is not a cost in any profile, so it does not help the service.
- Rover's profile at 1 core: 89% kernel, 9% user, 1% vdso. The response write is
  73% of the core. On loopback that write also runs the TCP receive path of the
  client and its wakeup. Thus a rewrite in C or Rust can gain at most about 10%
  on this path.

Only as notes, not measured:

- io_uring: it batches the submission of reads and writes and removes the
  epoll_wait calls. The TCP work for each packet stays. With 89% of the time in
  the TCP path, the gain is small for one request for each round trip. Go has no
  io_uring in the standard library, and a dependency or raw syscalls would be
  necessary.
- Kernel TLS: the service has no TLS. The CDN ends TLS. kTLS helps only when the
  origin encrypts and uses sendfile.
- DPDK or AF_XDP: they skip the kernel TCP stack, which is 89% of the time. They
  need a user-space TCP stack, a dedicated NIC and busy cores, and they do not
  work on loopback or in a normal Kubernetes pod.

## Final numbers against the ceilings

| Path | Final | Ceiling | Percentage |
|---|---|---|---|
| Small, 2 cores | 78.6k RPS | net/http floor 95k | 83% |
| Small, 2 cores | 78.6k RPS | raw Go sockets 170k | 46% |
| Small, 1 core | 43.1k RPS | net/http floor 54k | 80% |
| Small, 1 core, rover | 116.2k RPS | raw Go sockets 108k | 108% |
| Syscalls for each small response | 2.03 | 2 | at the floor |
| Large, over TCP | 5.81 GB/s | raw writes 8.0 to 8.9 GB/s | 65 to 72% |
| Generator, 3 and 5 | 11.5 GiB/s | memcpy in cache 40.9 GiB/s | 28% |

## What limits each path now

- Small responses, service: net/http. The service is within 20% of the net/http
  floor. The remaining handler work is about 1.5 µs of the 25 µs of core time
  for each request. The rest is net/http (about 15 allocations, a goroutine for
  each request, header cloning) and the kernel. 2 times more needs a different
  HTTP stack: rover gives 2.7 times on one core.
- Small responses, rover: the kernel TCP stack on loopback (89%). 2 times more
  needs fewer round trips (HTTP pipelining or HTTP/2 multiplexing by the
  clients), or a real NIC, where the client work is not on the server cores.
- Large responses: the kernel copy, the receive side of the client, and the
  hole stores. The generator is faster than loopback TCP. 2 times more needs
  fewer bytes (a precompressed body), or zero-copy to a real NIC. On loopback
  the receiver copies each byte anyway.

## If someone wants to merge some of this

The three changes that give the most for the least code:

1. The batched, hand-written request log (commits "write the request log line by
   hand" and "batch request log lines"). About 190 lines with the fuzz test.
   About +40% RPS with stdout as a pipe. Cost: up to 100 ms of log lines lost on a
   crash.
2. The block templates (commits "block templates for the large path" and
   "blocks of about 1 MiB"). About 200 lines. 3.4 times the large-response
   throughput. The existing fuzz targets and TestBlocks cover it.
3. GOGC=400 and PGO from a load-test profile. One line and one file. +10% RPS
   together. Cost: 11 MB of RSS and a profile to refresh.

The allocation changes (plan, parser, headers, Content-Length, stack plan,
deadline writer) together give −38% in-process (2.50 to 1.54 µs) and about +10%
over TCP (the sum of the significant steps). They are small and safe, but each one
alone is near the resolution of the load test.

Do not merge rover or items 8 and 11. They replace or depend on the internals of
the HTTP stack.

## Rules of AGENTS.md and the README that this branch breaks

- **Contract drift, not documented until now: the write deadline.** The README
  says that each 32 KiB write of a response has a 10 s deadline. With block
  templates, one write is up to 1 MiB. A client must now accept 1 MiB in 10 s
  (about 100 KB/s), not 32 KiB (3.2 KB/s). Slow clients can be disconnected.
  A fix: write blocks in 32 KiB pieces. The raw ceiling at 32 KiB writes is
  8.8 GB/s, so the cost is probably small, but it is not measured.
- "The README is the API contract": the README Performance section still
  describes the 32 KiB stream buffer and the old M1 numbers. The batched log
  (commit "batch request log lines") changed the log without a README change.
- "Put each new setting in config.go and in the README table": GOGC=400 is an
  ENV line in the Dockerfile, not a setting in config.go.
- "PGO and GOARM64 failed this test and are not in the build": default.pgo is
  in the build now, with new evidence. AGENTS.md is not updated.
- "Remove an optimization that does not measure faster": items 4, 8 and 10 are
  faster in-process only; their TCP gain is below the resolution. Item 26 is
  kept against the rule, by the owner's decision.
- "Each statsStore must pass TestStoreContract and TestStoreSaturation": the
  sharded statistics of rover are not a statsStore and do not run these tests.
  The same request on two loops costs two entries of the budget.
- "Easy to maintain" (the brief): item 8 depends on an unexported net/http
  constant; rover copies the parser and duplicates the HTTP layer; the block
  templates add 200 lines of dense code.
- The AGENTS.md map and "How agents helped" do not list `research/`.

## Notes

- Commit 92132b6 also contains the first version of `research/rover` by mistake
  (a `git add -A`). The rover commit 9531b88 says so.
- AGENTS.md says that PGO failed. This branch measures a gain with a profile from
  the load test. AGENTS.md is not changed, because the branch will not be
  merged.
- The fasthttp and gnet sub-branch is not done. After rover, the user asked to
  stop at the Go level, because deeper Linux work belongs in C or Rust.
