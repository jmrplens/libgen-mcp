# Scaling and capacity

**Explanation** — for an operator sizing a shared deployment.

This page will explain what bounds one process — the calls and sessions it may
hold, derived from its file-descriptor limit — how the per-caller limits and the
outbound request budget interact, and when running more than one replica helps
and when it only moves the queue. The measured figures behind it are in
[What it costs to run](../benchmarks/resource-benchmark.md).
