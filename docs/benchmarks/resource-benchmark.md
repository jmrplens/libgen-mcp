# What this server costs to run

**Reference** - for an operator sizing a deployment.

Measured on 2026-09-22T00:42:49Z, from the real binary on both transports, against an
in-process stand-in for the catalog on loopback. The run is offline, so a
second machine measures this server rather than its own network.

- **Host**: AMD Ryzen 5 3550H with Radeon Vega Mobile Gfx, 8 logical CPUs, 61 GiB RAM, linux/amd64, kernel 6.1.0-53-amd64, go1.27.0
- **Build**: 1.7.3 (e572df62a1fc2d1896fdd9cd91f02cf2382539f5)
- **Binary**: 35.8 MiB on disk
- **Rounds**: 3 per method per client, sampled every 100 ms

A client is what a client is on each transport: on HTTP it is a distinct
address, which is what this server charges a caller by, and on stdio it is a
process, because a client that wants a stdio server starts one.

## Startup and surface

| Scenario            | Transport | Clients | Ready (ms) | First list (ms) | Warm list (ms) | List bytes |
| ------------------- | --------- | ------: | ---------: | --------------: | -------------: | ---------: |
| stdio-1             | stdio     |       1 |      22.70 |            5.74 |           3.60 |      26897 |
| stdio-8             | stdio     |       8 |      20.56 |            6.25 |           3.96 |      26897 |
| http-1              | http      |       1 |      24.87 |            4.15 |           3.40 |      20609 |
| http-8              | http      |       8 |      26.33 |            4.60 |           4.39 |      20609 |
| http-8-otel         | http      |       8 |      32.67 |           15.02 |           4.26 |      20609 |
| http-8-shipped-rate | http      |       8 |      29.50 |            3.08 |           4.39 |      20609 |

**Ready** is spawn to a process that answers; **first list** is what the first
client waits for, which is what pays for registration. They are reported apart
because they moved apart: one number would hide the one that hurts.

## Memory

| Scenario            | Idle (MiB) | Mean (MiB) | Peak (MiB) | CPU ms/call |
| ------------------- | ---------: | ---------: | ---------: | ----------: |
| stdio-1             |      21.47 |      24.39 |      24.39 |        7.78 |
| stdio-8             |     172.65 |     204.02 |     204.21 |        7.92 |
| http-1              |      24.14 |      26.41 |      26.41 |        7.78 |
| http-8              |      22.87 |      36.79 |      39.00 |        7.85 |
| http-8-otel         |      25.03 |      43.82 |      44.05 |        7.29 |
| http-8-shipped-rate |      24.30 |      29.58 |      30.44 |       11.46 |

The resident set, read from the kernel rather than from inside the process: a
container limit is set against the resident set, and Go's own heap figure is a
different and smaller number. **Peak** is what a limit has to survive.

## Latency per method

| Scenario            | Method         | Calls | Outbound rps | Burst | p50 (ms) | p99 (ms) | max (ms) |
| ------------------- | -------------- | ----: | -----------: | ----: | -------: | -------: | -------: |
| stdio-1             | `tools/list`   |     3 |           20 |   100 |     4.60 |     5.34 |     5.34 |
| stdio-1             | `prompts/list` |     3 |           20 |   100 |     0.69 |     0.73 |     0.73 |
| stdio-1             | `tools/call`   |     3 |           20 |   100 |     9.51 |    10.90 |    10.90 |
| stdio-8             | `tools/list`   |    24 |           20 |   100 |     7.32 |    27.76 |    27.76 |
| stdio-8             | `prompts/list` |    24 |           20 |   100 |     1.05 |     5.55 |     5.55 |
| stdio-8             | `tools/call`   |    24 |           20 |   100 |    21.09 |    27.88 |    27.88 |
| http-1              | `tools/list`   |     3 |           20 |   100 |     4.29 |     5.06 |     5.06 |
| http-1              | `prompts/list` |     3 |           20 |   100 |     0.77 |     0.87 |     0.87 |
| http-1              | `tools/call`   |     3 |           20 |   100 |     9.97 |    12.53 |    12.53 |
| http-8              | `tools/list`   |    48 |           20 |   100 |    15.42 |    44.99 |    44.99 |
| http-8              | `prompts/list` |    48 |           20 |   100 |     3.70 |     8.54 |     8.54 |
| http-8              | `tools/call`   |    48 |           20 |   100 |    38.36 |    94.71 |    94.71 |
| http-8-otel         | `tools/list`   |    48 |           20 |   100 |    15.45 |    39.39 |    39.39 |
| http-8-otel         | `prompts/list` |    48 |           20 |   100 |     4.55 |    14.07 |    14.07 |
| http-8-otel         | `tools/call`   |    48 |           20 |   100 |    33.80 |    68.78 |    68.78 |
| http-8-shipped-rate | `tools/list`   |    48 |            1 |     1 |     6.86 |    34.07 |    34.07 |
| http-8-shipped-rate | `prompts/list` |    48 |            1 |     1 |     1.84 |     7.95 |     7.95 |
| http-8-shipped-rate | `tools/call`   |    48 |            1 |     1 | 14982.47 | 15997.87 | 15997.87 |

Percentiles are nearest-rank, so every number published is one the run actually
observed rather than a point interpolated between two calls nobody made. No row
carries an error column because a scenario whose calls failed does not finish:
a search that cannot reach its catalog answers in under a millisecond, and
averaging that with a real one publishes an error path as a cost.

**The outbound budget is the number to read these against**, both halves of it.
`tools/call` reaches the catalog, and every catalog request waits for a token
from `LIBGEN_MCP_RATE_RPS` out of a bucket `LIBGEN_MCP_RATE_BURST` deep. It
ships at one per second with a burst of one. That
is why the shipped-rate scenario's tool calls take seconds while the same work
with the valve open takes milliseconds: what is being measured there is the
queue, not the server. An inbound rate limit set above the outbound bucket only
moves the queue; it does not shorten it.

## What each extra caller costs

Measured rather than extrapolated: one process, more client addresses at each
step, and a reading taken twice. **Under load** is what N callers cost while all
of them are calling, which is what a host has to survive. **Held** is what they
cost with the load stopped and a collection forced, which is what a reader means
by the cost of another caller.

The load is `tools/list` rather than a tool call, and that is the honest choice
rather than a convenient one: a tool call reaches the catalog, every catalog
request takes a token from the outbound bucket, and a series driving flat out
exhausts it in the first step — measured, 2,758 of 2,817 calls came back refused
by the limiter. What that measures is the limiter. `tools/list` is served inside
the process, so it measures what this section is about, and it is also the call
every client really does make on every reconnect.

**series-http** — every client driving `tools/list`, 2 in flight each, 10 s per step.

| Clients | Mean (MiB) | Peak (MiB) | Calls | p50 (ms) | p99 (ms) | CPU ms/call | Held heap (MiB) | Settled RSS (MiB) |
| ------: | ---------: | ---------: | ----: | -------: | -------: | ----------: | --------------: | ----------------: |
|       1 |      26.12 |      27.57 |    50 |     6.04 |    12.49 |        8.20 |            1.98 |             26.41 |
|       2 |      28.92 |      30.03 |   100 |     7.76 |    14.79 |        8.20 |            2.00 |             26.63 |
|       5 |      31.37 |      32.39 |   250 |    18.30 |    41.40 |        9.72 |            2.18 |             27.48 |
|      10 |      32.82 |      35.36 |   500 |    28.96 |    49.01 |        9.08 |            2.38 |             28.88 |
|      25 |      36.05 |      40.12 |  1250 |    40.37 |    69.25 |        7.15 |            2.54 |             33.57 |
|      50 |      38.38 |      42.54 |  2500 |    43.53 |   142.97 |        6.03 |            3.03 |             31.54 |
|     100 |      45.54 |      53.38 |  5000 |    33.79 |   215.77 |        5.29 |            4.14 |             43.64 |
|     200 |      61.50 |      82.08 |  9371 |    91.32 |   629.32 |        5.00 |           20.77 |             65.18 |
|     500 |     125.25 |     140.32 |  7277 |   827.12 |  2596.26 |        5.44 |           45.09 |            112.23 |

Fitted across the steps: **0.22 MiB per caller under load** and **91 KiB per caller held**. The first is what a host needs while that many callers are all working at once;
the second is what holding one more costs with nothing in flight. Reading the first
as the second overstates a shared deployment by whatever its requests are carrying.

Every planned step ran, up to 500 clients.

## Notes from the run

- **stdio-1**: 3 requests reached the stand-in catalog
- **stdio-8**: 24 requests reached the stand-in catalog
- **http-1**: 3 requests reached the stand-in catalog
- **http-1**: measured build 1.7.3 (e572df62a1fc2d1896fdd9cd91f02cf2382539f5)
- **http-8**: 48 requests reached the stand-in catalog
- **http-8**: measured build 1.7.3 (e572df62a1fc2d1896fdd9cd91f02cf2382539f5)
- **http-8-otel**: 48 requests reached the stand-in catalog
- **http-8-otel**: 0 OTLP exports reached the collector during this scenario
- **http-8-otel**: measured build 1.7.3 (e572df62a1fc2d1896fdd9cd91f02cf2382539f5)
- **http-8-shipped-rate**: 48 requests reached the stand-in catalog
- **http-8-shipped-rate**: measured build 1.7.3 (e572df62a1fc2d1896fdd9cd91f02cf2382539f5)
