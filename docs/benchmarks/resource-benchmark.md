# What this server costs to run

**Reference** - for an operator sizing a deployment.

Measured on 2026-09-22T00:20:46Z, from the real binary on both transports, against an
in-process stand-in for the catalog on loopback. The run is offline, so a
second machine measures this server rather than its own network.

- **Host**: AMD Ryzen 5 3550H with Radeon Vega Mobile Gfx, 8 logical CPUs, 61 GiB RAM, linux/amd64, kernel 6.1.0-53-amd64, go1.27.0
- **Build**: 1.7.3 (b196ddde5ab6850d20de445ea78091d98d6d7c41)
- **Binary**: 35.8 MiB on disk
- **Rounds**: 3 per method per client, sampled every 100 ms

A client is what a client is on each transport: on HTTP it is a distinct
address, which is what this server charges a caller by, and on stdio it is a
process, because a client that wants a stdio server starts one.

## Startup and surface

| Scenario            | Transport | Clients | Ready (ms) | First list (ms) | Warm list (ms) | List bytes |
| ------------------- | --------- | ------: | ---------: | --------------: | -------------: | ---------: |
| stdio-1             | stdio     |       1 |      21.05 |            7.83 |           4.71 |      26897 |
| stdio-8             | stdio     |       8 |      20.51 |            5.53 |           2.87 |      26897 |
| http-1              | http      |       1 |      27.36 |            4.44 |           3.74 |      20609 |
| http-8              | http      |       8 |      27.74 |            3.67 |           4.26 |      20609 |
| http-8-otel         | http      |       8 |      29.15 |            5.05 |           3.13 |      20609 |
| http-8-shipped-rate | http      |       8 |      28.37 |            3.57 |           3.74 |      20609 |

**Ready** is spawn to a process that answers; **first list** is what the first
client waits for, which is what pays for registration. They are reported apart
because they moved apart: one number would hide the one that hurts.

## Memory

| Scenario            | Idle (MiB) | Mean (MiB) | Peak (MiB) | CPU ms/call |
| ------------------- | ---------: | ---------: | ---------: | ----------: |
| stdio-1             |      22.64 |      26.71 |      26.71 |        7.78 |
| stdio-8             |     171.92 |     201.73 |     202.18 |        9.03 |
| http-1              |      22.94 |      25.25 |      25.25 |        8.89 |
| http-8              |      24.14 |      34.92 |      36.96 |        7.85 |
| http-8-otel         |      24.66 |      44.05 |      47.84 |        7.36 |
| http-8-shipped-rate |      24.00 |      29.35 |      30.35 |       12.43 |

The resident set, read from the kernel rather than from inside the process: a
container limit is set against the resident set, and Go's own heap figure is a
different and smaller number. **Peak** is what a limit has to survive.

## Latency per method

| Scenario            | Method         | Calls | Outbound rps | Burst | p50 (ms) | p99 (ms) | max (ms) |
| ------------------- | -------------- | ----: | -----------: | ----: | -------: | -------: | -------: |
| stdio-1             | `tools/list`   |     3 |           20 |   100 |     4.46 |     4.66 |     4.66 |
| stdio-1             | `prompts/list` |     3 |           20 |   100 |     0.58 |     0.61 |     0.61 |
| stdio-1             | `tools/call`   |     3 |           20 |   100 |    10.35 |    13.55 |    13.55 |
| stdio-8             | `tools/list`   |    24 |           20 |   100 |     8.10 |    20.82 |    20.82 |
| stdio-8             | `prompts/list` |    24 |           20 |   100 |     1.43 |     9.09 |     9.09 |
| stdio-8             | `tools/call`   |    24 |           20 |   100 |    19.02 |    33.52 |    33.52 |
| http-1              | `tools/list`   |     3 |           20 |   100 |     4.45 |     5.43 |     5.43 |
| http-1              | `prompts/list` |     3 |           20 |   100 |     0.72 |     0.77 |     0.77 |
| http-1              | `tools/call`   |     3 |           20 |   100 |    10.40 |    10.86 |    10.86 |
| http-8              | `tools/list`   |    48 |           20 |   100 |    15.51 |    27.71 |    27.71 |
| http-8              | `prompts/list` |    48 |           20 |   100 |     4.63 |    16.35 |    16.35 |
| http-8              | `tools/call`   |    48 |           20 |   100 |    35.42 |    71.63 |    71.63 |
| http-8-otel         | `tools/list`   |    48 |           20 |   100 |    12.48 |    30.54 |    30.54 |
| http-8-otel         | `prompts/list` |    48 |           20 |   100 |     3.34 |    21.17 |    21.17 |
| http-8-otel         | `tools/call`   |    48 |           20 |   100 |    34.48 |    66.59 |    66.59 |
| http-8-shipped-rate | `tools/list`   |    48 |            1 |     1 |     7.73 |    30.72 |    30.72 |
| http-8-shipped-rate | `prompts/list` |    48 |            1 |     1 |     2.24 |     6.20 |     6.20 |
| http-8-shipped-rate | `tools/call`   |    48 |            1 |     1 | 14983.87 | 15994.65 | 15994.65 |

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
|       1 |      25.84 |      26.66 |    50 |     6.75 |     9.13 |        9.00 |            1.98 |             24.95 |
|       2 |      27.41 |      28.73 |   100 |     7.05 |    11.38 |        8.70 |            2.00 |             25.41 |
|       5 |      30.31 |      31.52 |   250 |    15.39 |    49.42 |        8.56 |            2.14 |             26.33 |
|      10 |      31.78 |      33.34 |   500 |    26.98 |    52.87 |        9.22 |            2.36 |             27.59 |
|      25 |      34.74 |      39.23 |  1250 |    41.58 |    82.05 |        7.22 |            2.55 |             28.88 |
|      50 |      37.97 |      45.34 |  2500 |    56.55 |   124.89 |        6.10 |            3.38 |             37.80 |
|     100 |      45.75 |      59.68 |  5000 |    54.14 |   184.53 |        5.30 |            4.14 |             41.75 |
|     200 |      63.23 |      74.70 |  9654 |   137.22 |   614.26 |        5.09 |           15.89 |             58.10 |
|     500 |     127.33 |     144.80 |  9051 |   450.75 |  2617.25 |        5.49 |           55.42 |             88.35 |

Fitted across the steps: **0.23 MiB per caller under load** and **113 KiB per caller held**. The first is what a host needs while that many callers are all working at once;
the second is what holding one more costs with nothing in flight. Reading the first
as the second overstates a shared deployment by whatever its requests are carrying.

Every planned step ran, up to 500 clients.

## Notes from the run

- **stdio-1**: 3 requests reached the stand-in catalog
- **stdio-8**: 24 requests reached the stand-in catalog
- **http-1**: 3 requests reached the stand-in catalog
- **http-1**: measured build 1.7.3 (b196ddde5ab6850d20de445ea78091d98d6d7c41)
- **http-8**: 48 requests reached the stand-in catalog
- **http-8**: measured build 1.7.3 (b196ddde5ab6850d20de445ea78091d98d6d7c41)
- **http-8-otel**: 48 requests reached the stand-in catalog
- **http-8-otel**: 0 OTLP exports reached the collector during this scenario
- **http-8-otel**: measured build 1.7.3 (b196ddde5ab6850d20de445ea78091d98d6d7c41)
- **http-8-shipped-rate**: 48 requests reached the stand-in catalog
- **http-8-shipped-rate**: measured build 1.7.3 (b196ddde5ab6850d20de445ea78091d98d6d7c41)
