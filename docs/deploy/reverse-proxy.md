# Behind a reverse proxy

**How-to guide** — for an operator putting the HTTP transport behind a proxy.

A reverse proxy in front of `libgen-mcp --http` terminates TLS, gives the endpoint a public name
and keeps a log. This page lists what the proxy has to do for the server to work behind it, then
gives a complete configuration for nginx, Caddy, Traefik, Apache httpd, HAProxy and Cloudflare
Tunnel. Every configuration here except Cloudflare's was run against the real binary; how is
[at the end](#how-these-configurations-were-tested).

What each server flag does, and why, is in [HTTP server mode](../http-server-mode.md). This page
says what a proxy needs and does not repeat it.

## What the proxy has to do

| Requirement                                                            | Without it                                                                                                                                              | Server side                                                                                        |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| Forward the client's `Host`, and declare that name to the server       | `403` with a JSON-RPC error `-40300`: `the Host header names a host this deployment does not serve`                                                     | `--public-url https://mcp.example.org`, or `--trusted-proxies` naming the proxy                    |
| Do not buffer the response                                             | Progress notifications of a long call arrive all at once, when the call ends                                                                            | Every `text/event-stream` response carries `X-Accel-Buffering: no`                                 |
| A read timeout longer than the longest call                            | The proxy cuts a call that is still running, with a `504` or a reset                                                                                    | A call may run up to `LIBGEN_MCP_ACTION_TIMEOUT` (`1h`)                                            |
| HTTP/1.1 or HTTP/2 to the upstream                                     | HTTP/1.0 has no chunked encoding, so a stream cannot be relayed as it is written                                                                        | Accepts both                                                                                       |
| Put the client's address in a header, and name the proxy               | Every caller is charged as the proxy: one rate-limit bucket, one in-flight count and one `user.hash` for everybody                                      | `--trusted-proxies` and `--trusted-proxy-header`, both or neither                                  |
| Add no CORS headers of its own                                         | Two `Access-Control-Allow-Origin` values, which a browser rejects. A proxy that answers `OPTIONS` itself also grants preflights the server would refuse | `--trusted-origins` decides, and the server answers the preflight                                  |
| A body limit at or above the server's                                  | nginx's default of 1 MiB refuses a request the server would take                                                                                        | `--max-request-body-bytes`, default 4 MiB, answered `413`                                          |
| Forward a path prefix only with `--http-path`                          | `404` for every route                                                                                                                                   | `--http-path /libgen` mounts the endpoint, `/mcp`, `/health` and the cards under it                |
| Probe `GET /health`                                                    | A balancer that never asks cannot see a drain                                                                                                           | `200` while serving, `503` with `{"status":"draining"}` during `--drain-delay`, any `Host` or none |
| Terminate TLS, or verify the server's certificate when the server does | Plain text on the wire, or a proxy that trusts any certificate                                                                                          | `--tls-cert` and `--tls-key` to terminate in the process                                           |

The rest of this section says why each row holds. The configurations follow it.

### The name the proxy forwards

The server refuses a `Host` header it was never told about, against DNS rebinding. A proxy that
forwards the client's `Host` and connects over loopback is exactly the shape that refusal is
about, so the request comes back `403`:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-40300,"message":"the Host header names a host this deployment does not serve. Behind a reverse proxy, pass --public-url with the origin clients use, or --trusted-proxies with the proxy's address."}}
```

`--public-url` declares the name and also puts a `remotes` entry on the discovery card, so it is
the one to use for a public endpoint. `--trusted-proxies` believes whatever `Host` a listed peer
forwards. Only the MCP endpoint is guarded: `/health` and the server cards answer any `Host`, and
a request with no `Host` at all, which is what HAProxy's `option httpchk` sends by default, is
served. A unix-socket listener names no host and is exempt. The rule in full is in
[The name clients use](../http-server-mode.md#the-name-clients-use-and-why-a-proxied-request-is-refused-without-it).

### Streaming and timeouts

A `tools/call` answers with a server-sent event stream when the client accepts one, which every
streamable HTTP client does. Two properties of that stream decide the proxy settings:

- **Nothing is written until the call has something to say.** The status line and the headers
  go out with the first event: a progress notification for a transfer, or the result. A search
  queued behind the outbound rate limit, or waiting on a slow mirror, sends no byte at all until
  it finishes. Measured against the server directly, a search held 35 seconds by a slow mirror
  sent its first byte at 35.0 seconds, and it arrived the same way through every proxy tested
  below. The proxy's read timeout therefore has to cover the whole call,
  not a gap between events. `LIBGEN_MCP_ACTION_TIMEOUT` (default `1h`) is what ends a call, so a
  one-hour proxy timeout matches it.
- **Once a stream has started, it is never silent for long.** The server writes a
  `: keep-alive` comment after 25 seconds without an event, so a stream that is open is never
  idle long enough for an idle timeout on the path to close it.

Buffering defeats the point of the stream: `download` and `read` report progress while bytes
move, and a buffering proxy delivers every notification at once, after the transfer. The server
sends `X-Accel-Buffering: no` on every stream, which nginx honours even where `proxy_buffering`
is on; the other proxies here either stream event streams by default or are told to below.
`--json-response` turns streams off altogether, at the cost of progress notifications.

### The caller's address

Three things in the server are per caller: the inbound rate limit, the in-flight ceiling on
`download` and `read`, and the `user.hash` telemetry attribute. Behind a proxy the connection
comes from the proxy, so all three see one caller until the server is told which header carries
the real address and which peer may set it:

```bash
--trusted-proxies 127.0.0.1/32 --trusted-proxy-header X-Real-IP
```

Both flags or neither: either one alone refuses to start. The header is read only from a listed
peer, and a multi-valued `X-Forwarded-For` is read from the right, stopping at the first hop that
is not itself a trusted proxy, so a caller cannot pick its own address. On a unix socket the
peer is a path, and the entry is the literal `unix`. Which address to list depends on where the
server runs, and **it is the address the server accepts connections from**, which behind a
published container port is the bridge gateway rather than `127.0.0.1`. The table for reading it
off your deployment is in
[What identity this deployment has](../http-server-mode.md#what-identity-this-deployment-has).

### CORS belongs to the server

A browser-based client needs the preflight answered and the origin echoed. The server does both
for the origins in `--trusted-origins`: it answers `OPTIONS` with `204`, echoes the origin, the
requested headers and `Vary`, and refuses a cross-origin `POST` from any other origin with `403`.
With the flag empty, the default, no browser origin is trusted.

So the proxy adds nothing. Two `Access-Control-Allow-Origin` headers on one response, one from
each layer, is a response every browser rejects while `curl` reports `200`, which is how this was
found on the hosted deployment. A proxy that answers `OPTIONS` itself is worse: it returns `204`
whatever the server would have said, and the browser then sends a `POST` the server refuses.
The one document a proxy answers CORS for is one it serves itself, such as an
[AI Catalog](../http-server-mode.md#publishing-an-ai-catalog).

### Paths

The server answers on its mount (`/` by default), on the `/mcp` alias, on `/health` and on the
server-card paths, and `404` with a JSON body naming the endpoint for anything else. A proxy can
publish the server under a prefix in two ways:

- **Strip the prefix**, so the server still sees `/`. Nothing to configure on the server.
- **Forward the prefix**, and mount the server under it with `--http-path /libgen`. The endpoint
  is then `POST /libgen` (and `/libgen/mcp`), the probe `GET /libgen/health`, and the cards
  `/libgen/server-card`, `/libgen/mcp/server-card` and `/libgen/.well-known/mcp/server-card.json`.
  Set `--public-url https://mcp.example.org/libgen` to match.

Forwarding is the simpler choice when the proxy also serves other things on the same host,
because the card's `remotes` URL and the paths a client derives from it stay consistent without a
rewrite.

### TLS at the proxy or in the process

When the proxy runs on the same machine, terminate TLS there and reach the server over loopback,
or better over a unix socket. When the proxy is on another host, the hop between them crosses a
network: let the server terminate TLS with `--tls-cert` and `--tls-key`, and have the proxy
verify that certificate. The server re-reads a renewed pair on the next handshake and sends
`Strict-Transport-Security` only when it terminates TLS itself; behind a proxy that terminates,
the proxy sends it. See
[Terminating TLS in this process](../http-server-mode.md#terminating-tls-in-this-process).

## The server behind every configuration

Every configuration below proxies to one of these two commands. Use the first for a TCP upstream
and the second for a unix socket:

```bash
libgen-mcp --http 127.0.0.1:8080 \
  --public-url https://mcp.example.org \
  --trusted-proxies 127.0.0.1/32 \
  --trusted-proxy-header X-Real-IP
```

```bash
libgen-mcp --http /run/libgen-mcp/libgen.sock \
  --public-url https://mcp.example.org \
  --trusted-proxies unix \
  --trusted-proxy-header X-Real-IP
```

The socket is created `0660`, so the proxy's worker processes need to be in the socket's group;
[Run as a service](service.md#a-unix-socket-with-a-dedicated-user) and
[Containers](containers.md#nginx-in-front-over-a-shared-unix-socket) set that up. Every flag has a
`LIBGEN_MCP_*` variable as well, listed in [Configuration](../configuration.md#http-listener).

## nginx

### TCP upstream

```nginx
upstream libgen_mcp {
    server 127.0.0.1:8080;
    keepalive 16;
}

server {
    listen 443 ssl;
    http2 on;
    server_name mcp.example.org;

    ssl_certificate     /etc/letsencrypt/live/mcp.example.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mcp.example.org/privkey.pem;

    # nginx refuses bodies over 1 MiB by default; keep this at or above
    # the server's --max-request-body-bytes (4 MiB).
    client_max_body_size 4m;

    location / {
        proxy_pass         http://libgen_mcp;
        proxy_http_version 1.1;
        proxy_set_header   Connection "";

        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;

        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

`proxy_http_version 1.1` with an empty `Connection` header is what lets nginx keep upstream
connections open (`keepalive 16`) and relay a chunked stream. `proxy_buffering off` is
belt-and-braces: the server's `X-Accel-Buffering: no` already turns buffering off per response.
nginx open source has no active health check; its `max_fails` only notices failed requests. See
[Rolling updates](scaling.md#rolling-updates) for what that means during a deploy.

### Unix-socket upstream

Change the upstream and nothing else:

```nginx
upstream libgen_mcp {
    server unix:/run/libgen-mcp/libgen.sock;
    keepalive 16;
}
```

nginx's workers must be able to open the `0660` socket and traverse its directory. On a host,
add nginx's user to the socket's group (`usermod -aG libgen-mcp www-data`) and restart nginx:
the workers call `initgroups()` when they drop privileges, so they pick up the group from
`/etc/group`. In a container, run the server with nginx's group instead, as
[Containers](containers.md#nginx-in-front-over-a-shared-unix-socket) does.

**The group argument of nginx's `user` directive is a name, not a number.** `user nginx 10001;`
fails at startup with `getgrnam("10001") failed` unless a group of that name exists in the proxy's
own `/etc/group`.

### TLS to the upstream

For a server on another host that terminates TLS itself
(`--tls-cert /etc/libgen-mcp/tls.crt --tls-key /etc/libgen-mcp/tls.key`), proxy to `https` and
verify its certificate:

```nginx
upstream libgen_mcp {
    server 10.0.0.12:8443;
    keepalive 16;
}

server {
    listen 443 ssl;
    http2 on;
    server_name mcp.example.org;

    ssl_certificate     /etc/letsencrypt/live/mcp.example.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mcp.example.org/privkey.pem;

    client_max_body_size 4m;

    location / {
        proxy_pass         https://libgen_mcp;
        proxy_http_version 1.1;
        proxy_set_header   Connection "";

        proxy_ssl_verify              on;
        proxy_ssl_trusted_certificate /etc/nginx/libgen-ca.pem;
        proxy_ssl_name                libgen.internal;
        proxy_ssl_server_name         on;
        proxy_ssl_session_reuse       on;

        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;

        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

nginx does not verify an upstream certificate unless told to, so without `proxy_ssl_verify on`
a wrong certificate goes unnoticed. With it, `proxy_ssl_trusted_certificate` has to name the CA
that issued the server's certificate, and `proxy_ssl_name` a name in that certificate, or every
request fails. On the server, `--trusted-proxies` names this nginx's address as the server sees
it.

### Under a path prefix

To forward `/libgen` unchanged, start the server with `--http-path /libgen` and
`--public-url https://mcp.example.org/libgen`, and give `proxy_pass` no URI, so nginx passes the
path as it came:

```nginx
location /libgen {
    proxy_pass         http://libgen_mcp;
    proxy_http_version 1.1;
    proxy_set_header   Connection "";
    proxy_set_header   Host              $host;
    proxy_set_header   X-Real-IP         $remote_addr;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;
    proxy_cache        off;
    proxy_read_timeout 1h;
    proxy_send_timeout 1h;
}
```

`POST /libgen`, `POST /libgen/mcp`, `GET /libgen/health` and the cards under `/libgen` all
answer through it, and `GET /health` without the prefix is a `404`.

## Caddy

```caddyfile
mcp.example.org {
    request_body {
        max_size 4MB
    }

    reverse_proxy 127.0.0.1:8080 {
        header_up X-Real-IP {remote_host}
        flush_interval -1
        transport http {
            read_timeout 1h
            write_timeout 1h
        }
    }
}
```

Caddy obtains the certificate itself, forwards the client's `Host` and sets `X-Forwarded-For`
by default. `header_up X-Real-IP {remote_host}` adds the single-valued header the server
commands above read; with `--trusted-proxy-header X-Forwarded-For` instead it can be dropped.
Caddy already flushes `text/event-stream` responses as they arrive, and `flush_interval -1`
makes that unconditional.

For a unix socket, the upstream is `reverse_proxy unix//run/libgen-mcp/libgen.sock { … }` with
the same block inside.

## Traefik

Traefik forwards the client's `Host`, sets both `X-Real-Ip` and `X-Forwarded-For` to the
client's address, and streams event-stream responses without buffering. It applies no timeout
to an upstream's response by default (`responseHeaderTimeout` is `0`), which is what a call that
sends no header for minutes needs: leave it at that, or set it above an hour. These
configurations use a TCP upstream.

### File provider

The static configuration, `/etc/traefik/traefik.yml`:

```yaml
entryPoints:
  web:
    address: ":80"
    http:
      redirections:
        entryPoint:
          to: websecure
          scheme: https
  websecure:
    address: ":443"

certificatesResolvers:
  letsencrypt:
    acme:
      email: you@example.org
      storage: /var/lib/traefik/acme.json
      httpChallenge:
        entryPoint: web

providers:
  file:
    filename: /etc/traefik/dynamic.yml
```

The dynamic configuration, `/etc/traefik/dynamic.yml`:

```yaml
http:
  routers:
    libgen-mcp:
      rule: "Host(`mcp.example.org`)"
      entryPoints: [websecure]
      service: libgen-mcp
      tls:
        certResolver: letsencrypt

  services:
    libgen-mcp:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: "http://127.0.0.1:8080"
        healthCheck:
          path: /health
          interval: 10s
          timeout: 3s
```

The health check takes a replica out of rotation as soon as its `/health` turns `503` during a
drain, so `--drain-delay 15s` covers one interval and the time to act on it.

### Docker labels

With Traefik and the server in one Compose project, the router lives on the server's labels.
The server sees Traefik's address on the shared network, so that address has to be fixed for
`--trusted-proxies` to name it:

```yaml
services:
  traefik:
    image: traefik:v3.6
    command:
      - --providers.docker=true
      - --providers.docker.exposedByDefault=false
      - --providers.docker.network=libgen_edge
      - --entryPoints.web.address=:80
      - --entryPoints.web.http.redirections.entryPoint.to=websecure
      - --entryPoints.websecure.address=:443
      - --certificatesResolvers.letsencrypt.acme.email=you@example.org
      - --certificatesResolvers.letsencrypt.acme.storage=/letsencrypt/acme.json
      - --certificatesResolvers.letsencrypt.acme.httpChallenge.entryPoint=web
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - letsencrypt:/letsencrypt
    networks:
      edge:
        ipv4_address: 172.30.0.2
    restart: unless-stopped

  libgen-mcp:
    image: ghcr.io/jmrplens/libgen-mcp:2.2.1
    command:
      - --http=0.0.0.0:8080
      - --public-url=https://mcp.example.org
      - --trusted-proxies=172.30.0.2/32
      - --trusted-proxy-header=X-Real-IP
      - --drain-delay=15s
    stop_grace_period: 30s
    networks: [edge]
    labels:
      - traefik.enable=true
      - traefik.http.routers.libgen-mcp.rule=Host(`mcp.example.org`)
      - traefik.http.routers.libgen-mcp.entrypoints=websecure
      - traefik.http.routers.libgen-mcp.tls.certresolver=letsencrypt
      - traefik.http.services.libgen-mcp.loadbalancer.server.port=8080
      - traefik.http.services.libgen-mcp.loadbalancer.healthcheck.path=/health
      - traefik.http.services.libgen-mcp.loadbalancer.healthcheck.interval=10s
    restart: unless-stopped

networks:
  edge:
    name: libgen_edge
    ipam:
      config:
        - subnet: 172.30.0.0/24
          ip_range: 172.30.0.128/25

volumes:
  letsencrypt:
```

`ip_range` keeps Docker's own address assignment in the upper half of the subnet. Without it,
whichever container starts first may take `172.30.0.2`, and Traefik then fails to start with
`Address already in use`. Pick a subnet no other network on the host uses.

On Docker Engine 29 the Docker provider needs Traefik 3.6 or later: 3.5 cannot talk to the
engine's API and logs `client version 1.24 is too old`, so no route is ever created.

## Apache httpd

```apache
LoadModule headers_module       modules/mod_headers.so
LoadModule proxy_module         modules/mod_proxy.so
LoadModule proxy_http_module    modules/mod_proxy_http.so
LoadModule ssl_module           modules/mod_ssl.so
LoadModule socache_shmcb_module modules/mod_socache_shmcb.so

Listen 443

<VirtualHost *:443>
    ServerName mcp.example.org

    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/mcp.example.org/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/mcp.example.org/privkey.pem

    ProxyPreserveHost On
    ProxyTimeout      3600
    LimitRequestBody  4194304

    RequestHeader set X-Real-IP "%{REMOTE_ADDR}s"
    RequestHeader set X-Forwarded-Proto "https"

    ProxyPass        "/" "http://127.0.0.1:8080/" flushpackets=on keepalive=On
    ProxyPassReverse "/" "http://127.0.0.1:8080/"
</VirtualHost>
```

`ProxyPreserveHost On` forwards the client's `Host`, the name `--public-url` declares, instead
of the upstream's address. `flushpackets=on` sends each chunk on as it arrives, and `mod_proxy` adds `X-Forwarded-For`
itself. On Debian the modules are enabled with `a2enmod proxy_http headers ssl` rather than
`LoadModule` lines. For a unix socket, the target is
`"unix:/run/libgen-mcp/libgen.sock|http://localhost/"` in both `ProxyPass` and
`ProxyPassReverse`, and Apache's user (`www-data`) goes in the socket's group.

## HAProxy

```haproxy
global
    log stdout format raw local0 warning

defaults
    mode http
    log global
    option httplog
    timeout connect      5s
    timeout http-request 10s
    timeout client       1h
    timeout server       1h
    timeout tunnel       1h

frontend mcp
    bind :443 ssl crt /etc/haproxy/certs/mcp.example.org.pem alpn h2,http/1.1
    http-request set-header X-Real-IP %[src]
    http-request set-header X-Forwarded-Proto https
    default_backend libgen_mcp

backend libgen_mcp
    option httpchk
    http-check send meth GET uri /health
    http-check expect status 200
    server mcp1 127.0.0.1:8080 check inter 10s
```

The `.pem` holds the certificate chain followed by the key. HAProxy forwards the client's `Host`
unchanged and does not buffer responses. `timeout server` is the one that matters for a long
call. The health check sends no `Host`, which the server answers. For a unix socket the server
line is `server mcp1 unix@/run/libgen-mcp/libgen.sock check inter 10s`, with HAProxy's user in
the socket's group.

## Cloudflare Tunnel

`cloudflared` runs next to the server and connects out to Cloudflare, so nothing listens on a
public address. Its `config.yml`:

```yaml
tunnel: 6ff42ae2-765d-4adf-8112-31c55c1551ef
credentials-file: /etc/cloudflared/6ff42ae2-765d-4adf-8112-31c55c1551ef.json

ingress:
  - hostname: mcp.example.org
    service: http://127.0.0.1:8080
    originRequest:
      connectTimeout: 10s
      keepAliveTimeout: 90s
  - service: http_status:404
```

The tunnel ID and its credentials file come from `cloudflared tunnel create`. Cloudflare passes
the client's address in `CF-Connecting-IP`, and `cloudflared` connects to the server from
loopback, so the server takes:

```bash
libgen-mcp --http 127.0.0.1:8080 \
  --public-url https://mcp.example.org \
  --trusted-proxies 127.0.0.1/32 \
  --trusted-proxy-header CF-Connecting-IP
```

**This configuration was not run end to end.** It was validated with
`cloudflared tunnel ingress validate` and `cloudflared tunnel ingress rule`, not served through
Cloudflare's edge. One limit matters more here than anywhere else: Cloudflare documents that a
proxied request whose origin has not started answering within 100 seconds fails with a `524`
on plans other than Enterprise. Since the server sends nothing until a call's first event, a
call that waits longer than that, such as a search queued behind the outbound rate limit, fails
at Cloudflare while the server goes on working on it.

## How these configurations were tested

Each configuration was run in the official image of its proxy, in front of the `libgen-mcp`
binary built from this repository, with the proxy on the host network and the server on
loopback or a unix socket. The only differences from the text above were the listening port,
the host name (`mcp.test.example`), a self-signed certificate in place of ACME, and, for Caddy, a
global `auto_https disable_redirects` because port 80 was taken. Each was checked for:

- `GET /health` through the proxy answering `200`.
- A `tools/list` `POST`, to the mount and to `/mcp`, answering `200` with `text/event-stream`.
- An unbuffered stream: a session's standalone `GET` stream delivered the server's
  `: keep-alive` comment 25 seconds after it opened, while the stream was still open, and a
  35-second search arrived intact rather than being cut by a timeout.
- A preflight from an origin in `--trusted-origins` answered `204` with that origin echoed, and a
  cross-origin `POST` from another origin refused with `403`.
- The public name accepted through the proxy with `--public-url` set, and refused with `403`
  when the server was started without it or `--trusted-proxies`.
- The caller's address: with a burst of two, a third `prompts/get` from one client address was
  refused with `-42900` while a call from a second address on the same proxy was served, which
  only happens when the forwarded header is believed.

| Proxy                  | Version  | Upstreams run                                   | Result                                                                                                                  |
| ---------------------- | -------- | ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| nginx                  | 1.29.8   | TCP, unix socket, TLS (private CA), path prefix | All checks pass                                                                                                         |
| Caddy                  | 2.10.2   | TCP, unix socket                                | All checks pass                                                                                                         |
| Traefik, file provider | 3.6.25   | TCP                                             | All checks pass                                                                                                         |
| Traefik, Docker labels | 3.6.25   | TCP, over a Compose network                     | Health, `tools/list`, preflight, `Host` and cards pass. The stream and caller checks were run on the file provider only |
| Apache httpd           | 2.4.69   | TCP, unix socket                                | All checks pass                                                                                                         |
| HAProxy                | 3.2.25   | TCP, unix socket                                | All checks pass, health check `UP` with no `Host`                                                                       |
| Cloudflare Tunnel      | 2025.9.1 | Configuration validated only                    | Not served through Cloudflare                                                                                           |

The stream check used `--stateless=false` for its standalone `GET`, since the default transport
has none. The same proxy settings carry a `POST` stream, whose timing the search check covered.
