# Behind a reverse proxy

**How-to guide** — for an operator putting the HTTP transport behind a proxy.

This page will hold complete reverse-proxy configurations for the HTTP
transport: which flags tell the server it is behind a proxy and which header
carries the caller's address, declaring the public URL, keeping streamed
responses unbuffered, and terminating TLS at the proxy or at the server. The
behaviour those configurations rely on is described in
[HTTP server mode](../http-server-mode.md).
