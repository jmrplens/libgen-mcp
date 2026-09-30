# Upstream bugs and gaps

Defects in projects this server depends on that this codebase works around or
has to accommodate, kept here so the workaround is not mistaken for design and
is removed when the fix ships.

**An entry is never deleted.** When the fix lands in a release this module
builds against, the entry is marked merged with that version and the
workaround retired, and the entry stays as the record of why the code looked
the way it did.

An entry earns its place by being **found from this codebase**: a workaround we
carry, a behavior a test had to accommodate, or a clause of the specification
we cannot satisfy because the dependency does not expose what it needs. A
setting of ours that produces the behavior is not one: the test before adding
an entry is whether it would happen to a caller who never configured anything.

**Link every tracker item in full**, with the project path in the link text,
for example
[modelcontextprotocol/go-sdk#1225](https://github.com/modelcontextprotocol/go-sdk/issues/1225).
A bare `#1225` is autolinked by GitHub against this repository, where it names
something unrelated or nothing.

## What each entry records

| Field          | Meaning                                                                              |
| -------------- | ------------------------------------------------------------------------------------ |
| **Reported**   | Whether it has been raised upstream, with a link                                     |
| **In review**  | Whether an upstream change was opened, with a link. It stays yes after the merge     |
| **Merged**     | Whether it has landed, and **in which release**                                      |
| **Blocking**   | Whether it blocks this server, or only costs a workaround                            |
| **Workaround** | Whether we carry one, where it lives, what retires it, and which test says it is due |

## Summary

| #   | Project | Issue                                                                                                               | Reported                                                                                                             | In review                                                                                                         | Merged              | Blocking                 | Workaround                          |
| --- | ------- | ------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | ------------------- | ------------------------ | ----------------------------------- |
| 1   | go-sdk  | [A tool result a middleware makes carries no `resultType`](#a-tool-result-a-middleware-makes-carries-no-resulttype) | Yes, by another user, [modelcontextprotocol/go-sdk#1225](https://github.com/modelcontextprotocol/go-sdk/issues/1225) | Yes, theirs, [modelcontextprotocol/go-sdk#1226](https://github.com/modelcontextprotocol/go-sdk/pull/1226), merged | **Yes, unreleased** | No, but it breaks a MUST | Yes, until the bump that carries it |

## go-sdk

### A tool result a middleware makes carries no `resultType`

- **Reported**: yes, by another user:
  [modelcontextprotocol/go-sdk#1225](https://github.com/modelcontextprotocol/go-sdk/issues/1225)
  describes this server's case exactly, a receiving middleware that answers a
  `tools/call` itself, as a rate limit or a concurrency ceiling does.
- **In review**: yes, theirs,
  [modelcontextprotocol/go-sdk#1226](https://github.com/modelcontextprotocol/go-sdk/pull/1226),
  merged as e40f35d.
- **Merged**: **yes, unreleased.** v1.8.0 is tagged four commits before the
  merge, so the v1.8.0 this module builds against does not carry it. v1.8.0 was
  still the newest tag on 2026-10-01.
- **Blocking**: no client is known to refuse the unlabeled result, but it
  breaks a MUST. Revision 2026-07-28 requires `resultType` on every result, and
  its rule that a client reads an absent field as `complete` covers only a
  server implementing an earlier revision.
- **Workaround**: yes. `toolutil.RefusalResult` is handed the request it is
  refusing and, when that request names 2026-07-28 or later in its `_meta`,
  builds the refusal with `resultType: "complete"`. The SDK keeps the field
  unexported, so the refusal is written in its wire form and read back through
  `CallToolResult.UnmarshalJSON`, which is the one public way to set it. An
  earlier revision gets no field, which is what the SDK sends such a client
  from its own dispatcher. The bump to the first tag carrying e40f35d retires
  the workaround: `TestSDKLeavesAMiddlewareMadeToolResultUnlabeled` in
  `internal/toolutil` fails on that bump and says what to delete.

**What**: go-sdk v1.8.0 labels a `tools/call` result only inside its own tool
dispatcher (`handleMultiRoundTripResult`, called from `Server.callTool`),
because such a result can also be `input_required`. The result types that can
only ever be complete are labeled after the middleware chain returns
(`setCompleteResultType`), and `CallToolResult` is not one of them. Two
middlewares here answer a `tools/call` before the dispatcher runs: the rate
limiter (`internal/toolutil/rate_limit.go`) and the per-caller ceiling on
downloads and reads (`cmd/server/ceiling.go`). Both use `RefusalResult`, so
before the workaround both refusals reached a 2026-07-28 client without
`resultType`, while a call the same server served carried `"complete"`.

**How we found it**: porting the pin the sibling project gitlab-mcp-server
keeps for the same defect. That project pins the absence and takes no
workaround, on the ground that the field cannot be set from outside the SDK.
The JSON round-trip above can, so this server conforms now instead.

**Tested by**: `TestRateLimitedToolCall_ResultTypeFollowsTheRevision` in
`test/e2e/http` drives the real binary at both revisions and asserts that the
limiter's refusal carries the same `resultType` as the call the dispatcher
served before it. It failed on the code before the workaround, with the
refusal's `resultType` absent at 2026-07-28. The ceiling's refusal is covered
by `TestTheCeilingRefusalCarriesResultTypeAtTheModernRevision` in
`cmd/server`, because reaching the ceiling over the wire needs a download held
in flight against a mirror.
