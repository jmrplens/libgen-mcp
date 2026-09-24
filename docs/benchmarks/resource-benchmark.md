# What this server costs to run

**Reference** - for an operator sizing a deployment.

Measured on 2026-09-24T10:01:38Z, from the real binary on both transports, against an
in-process stand-in for the catalog on loopback. The run is offline, so a
second machine measures this server rather than its own network.

- **Host**: AMD Ryzen 5 3550H with Radeon Vega Mobile Gfx, 8 logical CPUs, 61 GiB RAM, linux/amd64, kernel 6.1.0-53-amd64, go1.27.0
- **Build**: 2.0.1 (c5d9f3d1e07f4740e3e95ad00acb9579335331b1)
- **Binary**: 35.8 MiB on disk
- **Rounds**: 3 per method per client, sampled every 100 ms

A client is what a client is on each transport: on HTTP it is a distinct
address, which is what this server charges a caller by, and on stdio it is a
process, because a client that wants a stdio server starts one.

## Startup and surface

| Scenario            | Transport | Clients | Ready (ms) | First list (ms) | Warm list (ms) | List bytes |
| ------------------- | --------- | ------: | ---------: | --------------: | -------------: | ---------: |
| stdio-1             | stdio     |       1 |      25.92 |            6.30 |           4.79 |      26714 |
| stdio-8             | stdio     |       8 |      19.92 |            4.67 |           3.97 |      26714 |
| http-1              | http      |       1 |      27.86 |            5.48 |           5.29 |      20552 |
| http-8              | http      |       8 |      27.36 |            4.52 |           3.98 |      20552 |
| http-8-otel         | http      |       8 |      31.40 |            5.34 |           3.27 |      20552 |
| http-8-shipped-rate | http      |       8 |      70.39 |            6.01 |           3.84 |      20552 |

**Ready** is spawn to a process that answers; **first list** is what the first
client waits for, which is what pays for registration. They are reported apart
because they moved apart: one number would hide the one that hurts.

## Memory

| Scenario            | Idle (MiB) | Mean (MiB) | Peak (MiB) | CPU ms/call |
| ------------------- | ---------: | ---------: | ---------: | ----------: |
| stdio-1             |      21.44 |      26.67 |      26.67 |        8.89 |
| stdio-8             |     173.18 |     210.41 |     214.04 |        8.61 |
| http-1              |      23.54 |      25.41 |      25.41 |        6.67 |
| http-8              |      24.23 |      36.58 |      38.46 |        7.99 |
| http-8-otel         |      25.26 |      42.76 |      43.71 |        7.50 |
| http-8-shipped-rate |      25.78 |      29.99 |      30.86 |       10.69 |

The resident set, read from the kernel rather than from inside the process: a
container limit is set against the resident set, and Go's own heap figure is a
different and smaller number. **Peak** is what a limit has to survive.

## Latency per method

| Scenario            | Method         | Calls | Outbound rps | Burst | p50 (ms) | p99 (ms) | max (ms) |
| ------------------- | -------------- | ----: | -----------: | ----: | -------: | -------: | -------: |
| stdio-1             | `tools/list`   |     3 |           20 |   100 |     3.59 |     4.41 |     4.41 |
| stdio-1             | `prompts/list` |     3 |           20 |   100 |     0.68 |     0.97 |     0.97 |
| stdio-1             | `tools/call`   |     3 |           20 |   100 |    10.58 |    11.48 |    11.48 |
| stdio-8             | `tools/list`   |    24 |           20 |   100 |     7.24 |    17.94 |    17.94 |
| stdio-8             | `prompts/list` |    24 |           20 |   100 |     1.96 |    12.68 |    12.68 |
| stdio-8             | `tools/call`   |    24 |           20 |   100 |    23.32 |    52.46 |    52.46 |
| http-1              | `tools/list`   |     3 |           20 |   100 |     3.62 |     5.47 |     5.47 |
| http-1              | `prompts/list` |     3 |           20 |   100 |     0.83 |     1.79 |     1.79 |
| http-1              | `tools/call`   |     3 |           20 |   100 |    10.68 |    11.12 |    11.12 |
| http-8              | `tools/list`   |    48 |           20 |   100 |    18.92 |    49.13 |    49.13 |
| http-8              | `prompts/list` |    48 |           20 |   100 |     7.17 |    17.16 |    17.16 |
| http-8              | `tools/call`   |    48 |           20 |   100 |    47.54 |    81.33 |    81.33 |
| http-8-otel         | `tools/list`   |    48 |           20 |   100 |    18.40 |    67.18 |    67.18 |
| http-8-otel         | `prompts/list` |    48 |           20 |   100 |     5.99 |    56.69 |    56.69 |
| http-8-otel         | `tools/call`   |    48 |           20 |   100 |    58.25 |   144.82 |   144.82 |
| http-8-shipped-rate | `tools/list`   |    48 |            1 |     1 |     6.85 |    52.29 |    52.29 |
| http-8-shipped-rate | `prompts/list` |    48 |            1 |     1 |     1.83 |    12.61 |    12.61 |
| http-8-shipped-rate | `tools/call`   |    48 |            1 |     1 | 13996.30 | 16002.78 | 16002.78 |

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
|       1 |      26.99 |      27.90 |    50 |     5.48 |     8.89 |        7.80 |            1.96 |             25.43 |
|       2 |      28.68 |      29.59 |   100 |     7.28 |    10.94 |        8.80 |            2.05 |             27.07 |
|       5 |      30.75 |      32.52 |   250 |    13.68 |    36.99 |        7.76 |            2.26 |             27.74 |
|      10 |      32.98 |      34.46 |   500 |    22.26 |    68.38 |        7.46 |            2.47 |             29.56 |
|      25 |      35.49 |      39.71 |  1250 |    43.36 |    78.83 |        6.52 |            2.60 |             30.64 |
|      50 |      38.42 |      43.51 |  2500 |    39.58 |   111.38 |        5.70 |            3.16 |             32.68 |
|     100 |      45.61 |      53.64 |  5000 |    48.55 |   211.03 |        5.07 |            4.17 |             34.77 |
|     200 |      67.45 |      83.43 |  9116 |   177.78 |   695.72 |        5.10 |           21.39 |             67.98 |
|     500 |     126.65 |     139.96 |  8534 |   830.15 |  1911.12 |        5.30 |           31.51 |             83.81 |

Fitted across the steps: **0.22 MiB per caller under load** and **65 KiB per caller held**. The first is what a host needs while that many callers are all working at once;
the second is what holding one more costs with nothing in flight. Reading the first
as the second overstates a shared deployment by whatever its requests are carrying.

Every planned step ran, up to 500 clients.

## Notes from the run

- **stdio-1**: 3 requests reached the stand-in catalog
- **stdio-8**: 24 requests reached the stand-in catalog
- **http-1**: 3 requests reached the stand-in catalog
- **http-1**: measured build 2.0.1 (c5d9f3d1e07f4740e3e95ad00acb9579335331b1)
- **http-8**: 48 requests reached the stand-in catalog
- **http-8**: measured build 2.0.1 (c5d9f3d1e07f4740e3e95ad00acb9579335331b1)
- **http-8-otel**: 48 requests reached the stand-in catalog
- **http-8-otel**: 0 OTLP exports reached the collector during this scenario
- **http-8-otel**: measured build 2.0.1 (c5d9f3d1e07f4740e3e95ad00acb9579335331b1)
- **http-8-shipped-rate**: 48 requests reached the stand-in catalog
- **http-8-shipped-rate**: measured build 2.0.1 (c5d9f3d1e07f4740e3e95ad00acb9579335331b1)
