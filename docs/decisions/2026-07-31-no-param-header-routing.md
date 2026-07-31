# No SEP-2243 param-header routing on the tool surface

Date: 2026-07-31

No tool argument carries an `x-mcp-header` annotation, and none should without
re-checking what follows. This records why, because the opposite looks obviously
right: `md5` addresses exactly one file, which makes it read like the routable
key of the whole surface.

## What the annotation actually does

[SEP-2243][sep] lets a tool's input schema name an HTTP header that mirrors one
of its arguments, so an intermediary can shard, cache or rate-limit on that value
without parsing the JSON-RPC body. The name in the schema is the **bare** suffix
(`Md5`); the transport prepends `Mcp-Param-` itself.

It is not a passive hint. Measured against the real streamable-HTTP transport at
protocol `2026-07-28`:

| Request                         | Result                                |
| ------------------------------- | ------------------------------------- |
| argument present, header absent | `-32020 missing Mcp-Param-Md5 header` |
| header disagrees with the body  | `-32020 does not match body value`    |
| header mirrors the body         | accepted                              |

The server enforces the mirror. Requests on older protocol versions are not
affected.

## Why that is wrong for this server

Three findings, in the order they matter:

1. **Browser clients cannot satisfy it.** Dynamically named headers cannot be
   statically allow-listed for credentialed CORS, so browser environments skip
   the mirroring — while a conforming server still rejects the call. The
   TypeScript SDK documents this in as many words: "calling such a tool with that
   argument from a browser is a known limitation." Annotating `md5` therefore
   makes `download` and `get_details` **uncallable by md5 from a browser-based
   client** over HTTP.
2. **It requires a prior `tools/list`.** Clients learn the annotation from the
   catalog and cache the argument→header map when they list. A client calling
   from a catalog it persisted earlier, without re-listing, sends no header and
   is rejected. (The TypeScript SDK offers a per-call `toolDefinition` override
   to work around this; not every client will use it.)
3. **The benefit is currently zero.** Nothing fronts this server — it is a local
   stdio binary, a container, and a single HTTP deployment. There is no gateway
   to route, and no auth or tiering that would make a routing key useful.

The mechanism is shaped for coarse, low-cardinality routing keys — the SDK's own
conformance fixture annotates a `region` argument. `md5` is the opposite: the
high-cardinality identifier that nearly every call to the surface carries. Making
the most-used argument in the surface conditional on a header some clients
structurally cannot send trades a real failure mode for a capability nobody is
using.

## Consequences

- `download`, `get_details` and `read` take `md5` as an ordinary body argument.
- `get_details` needs no explicit `InputSchema`; the SDK infers it, as before.
- `TestNoParamHeaderAnnotations` fails if an annotation reappears anywhere on the
  surface, so this decision is revisited on purpose rather than by accident.

## When to revisit

If this server ever runs behind a gateway that must route or cache without
reading bodies, **and** the clients that matter for that deployment are not
browser-based. At that point the annotation belongs on a coarse argument
introduced for the purpose, not on `md5`.

[sep]: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2243
