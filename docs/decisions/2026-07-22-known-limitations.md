# Known limitations (follow-ups)

Date: 2026-07-22

The `read` tool extracts text with a bounded, in-memory strategy. Two limits are
known and accepted for now; both are documented here as follow-up work rather
than blockers.

## 1. Text/EPUB extraction is capped at 8 MiB

`internal/extract/txt.go` and `internal/extract/epub.go` read each source
(the TXT file, or each EPUB spine document) through an `io.LimitReader` capped at
`maxTextFileBytes` (8 MiB) and paginate within that in-memory buffer. Content
beyond the cap is not reachable, even via `offset`/`cursor`. The current behavior
is honest about this: when the source saturates the cap, the returned `Chunk`
sets `Truncated = true` and appends a note to `Reason` ("document exceeds the
8 MiB extraction cap; text beyond it is not available").

Follow-up (not implemented): seek-based streaming so `offset`/pagination can
reach text past the 8 MiB cap without loading the whole document into memory,
plus an EPUB early-stop / aggregate-cap so follow-up pages do not re-decode the
entire book on every call.

## 2. Temp-cache eviction is insertion-triggered, not time-swept

`internal/libgen/tempcache.go` evicts stale entries when the next insertion runs,
not on an independent idle timer. An entry can therefore outlive its TTL until
the next insert. This is acceptable given the cache's small size and short TTL.

Follow-up (not implemented): a periodic idle eviction sweep so entries are
reclaimed on their TTL regardless of insertion activity.
