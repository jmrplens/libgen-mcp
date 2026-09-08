# A hosted server does not fetch file bodies

Date: 2026-09-08

`download` already had this right. On a remote deployment it resolves a direct
URL and returns it as a `resource_link`; the client fetches the file. `read` did
not: to return one page of text it pulled the entire book over the server's own
connection, and nothing could switch that off — `LIBGEN_MCP_REMOTE_DOWNLOADS`
only forced `download` into link mode, which `--http` already did.

This records the rule that fixed the asymmetry, the line it draws, and what was
deliberately left on the server's side of that line.

## The rule

> On a hosted deployment, the **body of a file** is fetched by the client, never
> by the server, unless the operator opts in.

`LIBGEN_MCP_SERVER_FETCH` carries it: off by default on a remote deployment
(`--http`, a unix socket, or `LIBGEN_MCP_REMOTE_DOWNLOADS=1`), on by default on
a local stdio server, and an explicit value wins in either direction.

## Why the file body specifically

A hosted server reaches the mirrors over an egress IP shared by every user it
serves. That address is the asset. If a mirror throttles or blocks it for
excessive traffic, the service is down for everybody, and nothing in the
deployment can undo the block. When the client fetches the file, the same
excess lands on the caller who caused it and is confined there.

The traffic that provokes that is sustained multi-megabyte transfers. It is not
the handful of small HTML and redirect requests it takes to work out where a
file lives, and it is not a catalog query. So the line is drawn at the body,
which is also the only place it can be drawn without emptying the server:

- **Link resolution stays.** The URL `download` returns is only obtainable by
  asking the mirror for it — LibGen's `key=` token is read off `ads.php`,
  `randombook` discovers a mirror first, the article sources ask their
  registrars where a PDF is. A gate here would leave `download` with nothing to
  do, so the tool would lose its whole purpose rather than get safer.
- **`search` and `get_details` stay.** They cannot move client-side at all
  without emptying the server of its content. Gating them while keeping the
  tool that reaches the same mirrors would draw the line in a place that buys
  nothing.

The three are one judgment, not three exceptions: what leaves is the megabytes,
what stays is everything that is measured in kilobytes and cannot be done
anywhere else.

## Why `read` disappears rather than fails

With fetching off, `read` is not registered. It is absent from `tools/list`, and
the handshake instructions drop its step.

The alternative — keep it listed, refuse every call — was rejected because a
model that can see a tool calls it. Each attempt costs a turn to learn what the
tool list could have said for free, and the model has no way to remember the
refusal across sessions. Absence is the only form of the message that arrives
before the call rather than after it.

The refusal still exists one layer down, in `libgen.ErrServerFetchDisabled`,
which names the variable that lifts it and the tool that still works. It should
be unreachable through MCP; it is the second line, not the first.

## Consequences

- A remote deployment serves three tools, not four.
- `download` returns a link whenever fetching is off, including on a local
  server whose operator turned it off. Saving to disk is a fetch like any other.
- An operator with egress to spare sets `LIBGEN_MCP_SERVER_FETCH=1` and gets the
  fourth tool back. A private instance, or one whose egress is not shared, is
  exactly the case that default cannot know about and the operator can.
- A client that wants a file's text on a hosted deployment calls `download`,
  fetches the link with its own HTTP tool, and reads it locally. MCP has no way
  for a tool to push bytes to a client, so this was already the shape of
  `download`; `read` now matches it instead of contradicting it.
- The obvious follow-up — a `read` that works over HTTP by having the client
  fetch the bytes and send them back — is not part of this decision. It is a
  different mechanism with its own costs, and it should be measured before it is
  designed.

## Where this lives

- `internal/config` — `ResolveServerFetch` holds the defaults and why they
  differ by transport.
- `internal/libgen` — `ErrServerFetchDisabled` documents the boundary above, and
  guards `DownloadItem` and `FetchToTemp`.
- `internal/tools` — `WithoutServerFetch` drops `read` and puts `download` in
  link-only mode.
- `cmd/server` — resolves the tri-state against the transport, the one place
  that knows it.
