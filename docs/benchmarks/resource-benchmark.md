# What this server costs to run

**Reference** - for an operator sizing a deployment.

Measured on 2026-09-21T23:12:18Z, from the real binary on both transports, against an
in-process stand-in for the catalog on loopback. The run is offline, so a
second machine measures this server rather than its own network.

- **Host**: AMD Ryzen 5 3550H with Radeon Vega Mobile Gfx, 8 logical CPUs, 61 GiB RAM, linux/amd64, kernel 6.1.0-53-amd64, go1.27.0
- **Build**: 1.7.3
- **Binary**: 35.8 MiB on disk
- **Rounds**: 3 per method per client, sampled every 100 ms

A client is what a client is on each transport: on HTTP it is a distinct
address, which is what this server charges a caller by, and on stdio it is a
process, because a client that wants a stdio server starts one.

## Startup and surface

| Scenario            | Transport | Clients | Ready (ms) | First list (ms) | Warm list (ms) | List bytes |
| ------------------- | --------- | ------: | ---------: | --------------: | -------------: | ---------: |
| stdio-1             | stdio     |       1 |      24.28 |            6.24 |           7.23 |      26897 |
| stdio-8             | stdio     |       8 |      19.45 |            4.29 |           4.04 |      26897 |
| http-1              | http      |       1 |      28.03 |            4.58 |           3.40 |      20609 |
| http-8              | http      |       8 |      26.06 |            5.01 |           3.73 |      20609 |
| http-8-otel         | http      |       8 |      33.28 |            3.94 |           4.53 |      20609 |
| http-8-shipped-rate | http      |       8 |      32.58 |            3.56 |           3.98 |      20609 |

**Ready** is spawn to a process that answers; **first list** is what the first
client waits for, which is what pays for registration. They are reported apart
because they moved apart: one number would hide the one that hurts.

## Memory

| Scenario            | Idle (MiB) | Mean (MiB) | Peak (MiB) | CPU ms/call |
| ------------------- | ---------: | ---------: | ---------: | ----------: |
| stdio-1             |      20.86 |      23.49 |      23.49 |       12.22 |
| stdio-8             |     175.77 |     208.81 |     209.80 |        8.33 |
| http-1              |      22.75 |      26.16 |      26.16 |        7.78 |
| http-8              |      23.78 |      37.39 |      40.88 |        7.85 |
| http-8-otel         |      26.17 |      41.62 |      43.00 |        7.29 |
| http-8-shipped-rate |      23.38 |      29.09 |      30.61 |       11.46 |

The resident set, read from the kernel rather than from inside the process: a
container limit is set against the resident set, and Go's own heap figure is a
different and smaller number. **Peak** is what a limit has to survive.

## Latency per method

| Scenario            | Method         | Calls | Outbound rps | p50 (ms) | p99 (ms) | max (ms) |
| ------------------- | -------------- | ----: | -----------: | -------: | -------: | -------: |
| stdio-1             | `tools/list`   |     3 |           20 |     7.03 |     7.04 |     7.04 |
| stdio-1             | `prompts/list` |     3 |           20 |     0.73 |     0.78 |     0.78 |
| stdio-1             | `tools/call`   |     3 |           20 |    15.86 |    16.81 |    16.81 |
| stdio-8             | `tools/list`   |    24 |           20 |     8.75 |    15.63 |    15.63 |
| stdio-8             | `prompts/list` |    24 |           20 |     0.85 |     4.17 |     4.17 |
| stdio-8             | `tools/call`   |    24 |           20 |    19.83 |    31.23 |    31.23 |
| http-1              | `tools/list`   |     3 |           20 |     4.20 |     4.23 |     4.23 |
| http-1              | `prompts/list` |     3 |           20 |     1.01 |     1.10 |     1.10 |
| http-1              | `tools/call`   |     3 |           20 |     9.40 |    10.47 |    10.47 |
| http-8              | `tools/list`   |    48 |           20 |    18.42 |    38.81 |    38.81 |
| http-8              | `prompts/list` |    48 |           20 |     4.34 |    14.35 |    14.35 |
| http-8              | `tools/call`   |    48 |           20 |    37.96 |    74.09 |    74.09 |
| http-8-otel         | `tools/list`   |    48 |           20 |    13.64 |    29.03 |    29.03 |
| http-8-otel         | `prompts/list` |    48 |           20 |     4.50 |    16.78 |    16.78 |
| http-8-otel         | `tools/call`   |    48 |           20 |    33.78 |    53.10 |    53.10 |
| http-8-shipped-rate | `tools/list`   |    48 |            1 |     6.66 |    27.63 |    27.63 |
| http-8-shipped-rate | `prompts/list` |    48 |            1 |     1.44 |    10.51 |    10.51 |
| http-8-shipped-rate | `tools/call`   |    48 |            1 | 14986.73 | 16002.87 | 16002.87 |

Percentiles are nearest-rank, so every number published is one the run actually
observed rather than a point interpolated between two calls nobody made. No row
carries an error column because a scenario whose calls failed does not finish:
a search that cannot reach its catalog answers in under a millisecond, and
averaging that with a real one publishes an error path as a cost.

**The outbound budget is the number to read these against.** `tools/call` reaches
the catalog, and every catalog request waits for a token from
`LIBGEN_MCP_RATE_RPS`, which ships at one per second with a burst of one. That
is why the shipped-rate scenario's tool calls take seconds while the same work
with the valve open takes milliseconds: what is being measured there is the
queue, not the server. An inbound rate limit set above the outbound bucket only
moves the queue; it does not shorten it.

## Notes from the run

- **stdio-1**: 3 requests reached the stand-in catalog
- **stdio-8**: 24 requests reached the stand-in catalog
- **http-1**: 3 requests reached the stand-in catalog
- **http-1**: measured build 1.7.3
- **http-8**: 48 requests reached the stand-in catalog
- **http-8**: measured build 1.7.3
- **http-8-otel**: 48 requests reached the stand-in catalog
- **http-8-otel**: 0 OTLP exports reached the collector during this scenario
- **http-8-otel**: measured build 1.7.3
- **http-8-shipped-rate**: 48 requests reached the stand-in catalog
- **http-8-shipped-rate**: measured build 1.7.3
