# Run as a service

**How-to guide** — for an operator keeping the HTTP server running on a host.

This page will cover running the server as a long-lived system service: a
complete unit file, listening on a TCP port or a unix socket, the environment
file, how a stop signal drains calls in flight, and the health check a
supervisor can run. The behaviour each setting controls is described in
[HTTP server mode](../http-server-mode.md).
