# Rover results

Rover is the service without net/http and gin (see main.go). TestMatchesService
compares every response byte with the real service, except the Date value.

Machine: 4-core Intel Xeon 2.1 GHz (KVM), Linux 6.18, Go 1.27. wrk on the other
cores, keep-alive, limit=100 unless noted. Each line is one 5 s run.

## One server core (wrk on 3 cores, 96 connections)

| Server | RPS, run 1 | RPS, run 2 |
|---|---|---|
| Service at the start (baseline) | 28,751 | 27,064 |
| Service now (all KEEP changes, GOGC=400) | 39,256 | 41,948 |
| net/http floor (fixed body, no work) | 56,067 | 51,641 |
| Raw Go sockets, goroutine per connection (fixed body) | 106,184 | 110,476 |
| Rover, with log and response cache | 118,165 | 114,715 |
| Rover, no log | 135,219 | 128,394 |
| Rover, busy polling (SPIN=1) | 116,674 | 109,894 |

## Two server cores (wrk on 2 cores, 64 connections)

| Server | RPS | p50 | p99 |
|---|---|---|---|
| Service now | 80,267 | 770 us | 3.0 ms |
| Rover, log and cache | 279,069 | 172 us | 1.8 ms |
| Rover, no cache | 197,989 | 281 us | 1.8 ms |
| Rover, pinned | 283,050 | 164 us | 1.2 ms |

At two cores wrk also uses its two cores fully, so these numbers are a limit of
the machine, not only of rover.

## Where the time goes (one core, perf, cpu-clock)

89% kernel, 9% rover, 1% vdso. The response write is 73%. On loopback it
includes the TCP receive work of the client and its wakeup. A faster user space,
in any language, can gain at most about 10%.

## Large responses (limit=10,000,000, 4 connections, 2 server cores)

| Server | GB/s |
|---|---|
| Service now | 5.6 |
| Rover (SO_SNDBUF default, 1 MiB, 4 MiB) | 5.3 to 5.6 |
| Raw writes of a fixed buffer, 32 KiB | 8.2 to 8.9 |

Both use the block-template generator, so rover gains nothing here. wrk uses
its two cores fully, and the server spends 19% in the kernel copy, 17% in the
hole stores and 15% in wakeups.
