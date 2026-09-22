# What this server costs to run

**Reference** - for an operator sizing a deployment.

Measured on 2026-09-21T23:54:35Z, from the real binary on both transports, against an
in-process stand-in for the catalog on loopback. The run is offline, so a
second machine measures this server rather than its own network.

- **Host**: AMD Ryzen 5 3550H with Radeon Vega Mobile Gfx, 8 logical CPUs, 61 GiB RAM, linux/amd64, kernel 6.1.0-53-amd64, go1.27.0
- **Build**: 1.7.3 (4eed6907cd11f7ee26edf549b8f69035207b7ae8)
- **Binary**: 35.8 MiB on disk
- **Rounds**: 3 per method per client, sampled every 100 ms

A client is what a client is on each transport: on HTTP it is a distinct
address, which is what this server charges a caller by, and on stdio it is a
process, because a client that wants a stdio server starts one.

## Startup and surface

| Scenario            | Transport | Clients | Ready (ms) | First list (ms) | Warm list (ms) | List bytes |
| ------------------- | --------- | ------: | ---------: | --------------: | -------------: | ---------: |
| stdio-1             | stdio     |       1 |      42.59 |            6.04 |           4.90 |      26897 |
| stdio-8             | stdio     |       8 |      21.52 |            5.54 |           4.22 |      26897 |
| http-1              | http      |       1 |      27.26 |            3.16 |           3.51 |      20609 |
| http-8              | http      |       8 |      26.60 |            4.53 |           4.06 |      20609 |
| http-8-otel         | http      |       8 |      29.80 |            5.95 |           3.07 |      20609 |
| http-8-shipped-rate | http      |       8 |      30.45 |            3.36 |           4.66 |      20609 |

**Ready** is spawn to a process that answers; **first list** is what the first
client waits for, which is what pays for registration. They are reported apart
because they moved apart: one number would hide the one that hurts.

## Memory

| Scenario            | Idle (MiB) | Mean (MiB) | Peak (MiB) | CPU ms/call |
| ------------------- | ---------: | ---------: | ---------: | ----------: |
| stdio-1             |      21.95 |      25.63 |      25.63 |        7.78 |
| stdio-8             |     173.59 |     204.88 |     208.20 |        9.17 |
| http-1              |      24.00 |      25.66 |      25.66 |        8.89 |
| http-8              |      22.47 |      35.42 |      40.37 |        7.92 |
| http-8-otel         |      25.85 |      42.78 |      45.63 |        7.43 |
| http-8-shipped-rate |      22.86 |      28.29 |      29.34 |       10.42 |

The resident set, read from the kernel rather than from inside the process: a
container limit is set against the resident set, and Go's own heap figure is a
different and smaller number. **Peak** is what a limit has to survive.

## Latency per method

| Scenario            | Method         | Calls | Outbound rps | Burst | p50 (ms) | p99 (ms) | max (ms) |
| ------------------- | -------------- | ----: | -----------: | ----: | -------: | -------: | -------: |
| stdio-1             | `tools/list`   |     3 |           20 |   100 |     4.28 |     5.46 |     5.46 |
| stdio-1             | `prompts/list` |     3 |           20 |   100 |     0.47 |     0.76 |     0.76 |
| stdio-1             | `tools/call`   |     3 |           20 |   100 |     9.20 |    14.99 |    14.99 |
| stdio-8             | `tools/list`   |    24 |           20 |   100 |    13.29 |    50.60 |    50.60 |
| stdio-8             | `prompts/list` |    24 |           20 |   100 |     1.52 |    12.54 |    12.54 |
| stdio-8             | `tools/call`   |    24 |           20 |   100 |    23.55 |    40.64 |    40.64 |
| http-1              | `tools/list`   |     3 |           20 |   100 |     3.64 |     3.90 |     3.90 |
| http-1              | `prompts/list` |     3 |           20 |   100 |     1.12 |     1.74 |     1.74 |
| http-1              | `tools/call`   |     3 |           20 |   100 |    10.00 |    10.12 |    10.12 |
| http-8              | `tools/list`   |    48 |           20 |   100 |    18.28 |    69.97 |    69.97 |
| http-8              | `prompts/list` |    48 |           20 |   100 |     5.86 |    19.10 |    19.10 |
| http-8              | `tools/call`   |    48 |           20 |   100 |    43.45 |    77.05 |    77.05 |
| http-8-otel         | `tools/list`   |    48 |           20 |   100 |    14.13 |    30.13 |    30.13 |
| http-8-otel         | `prompts/list` |    48 |           20 |   100 |     5.13 |    17.99 |    17.99 |
| http-8-otel         | `tools/call`   |    48 |           20 |   100 |    37.13 |    55.71 |    55.71 |
| http-8-shipped-rate | `tools/list`   |    48 |            1 |     1 |     7.35 |    33.26 |    33.26 |
| http-8-shipped-rate | `prompts/list` |    48 |            1 |     1 |     2.23 |     7.62 |     7.62 |
| http-8-shipped-rate | `tools/call`   |    48 |            1 |     1 | 14985.06 | 16005.32 | 16005.32 |

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

## Notes from the run

- **stdio-1**: 3 requests reached the stand-in catalog
- **stdio-8**: 24 requests reached the stand-in catalog
- **http-1**: 3 requests reached the stand-in catalog
- **http-1**: measured build 1.7.3 (4eed6907cd11f7ee26edf549b8f69035207b7ae8)
- **http-8**: 48 requests reached the stand-in catalog
- **http-8**: measured build 1.7.3 (4eed6907cd11f7ee26edf549b8f69035207b7ae8)
- **http-8-otel**: 48 requests reached the stand-in catalog
- **http-8-otel**: 0 OTLP exports reached the collector during this scenario
- **http-8-otel**: measured build 1.7.3 (4eed6907cd11f7ee26edf549b8f69035207b7ae8)
- **http-8-shipped-rate**: 48 requests reached the stand-in catalog
- **http-8-shipped-rate**: measured build 1.7.3 (4eed6907cd11f7ee26edf549b8f69035207b7ae8)
