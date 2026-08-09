# A tool result may reveal only what the call already revealed

Date: 2026-08-08

`download` used to name the source that served the file, the mirror host that
served the bytes, and — whenever the server happened to hold a membership key —
the remaining allowance on that account. None of the three was ever asked for.
This records the rule that removed them, because it now governs three separate
fields and will govern the next one.

## The rule

> A tool result may disclose only facts the call itself already disclosed.

A caller that pinned `source: "annas"` knows it asked for Anna's Archive, so
telling it whether that pin held adds nothing it did not already state. A caller
that pinned nothing never named a provider, so naming one back is new
information travelling in a direction nobody requested.

## Why provenance is the wrong thing to return

Three findings, in the order they matter.

1. **It leaves the machine.** A tool result is not a private channel. It is
   pasted into the model's context and shipped to whatever inference provider the
   client happens to use, where it may be logged or retained. `source: "scihub"`
   in a result is therefore a statement about the operator's configuration and
   the user's reading, published to a third party that was never party to the
   deployment. The operator chose which sources to enable; the user chose what to
   read; neither chose that.
2. **It buys the caller nothing actionable.** By the time the source name exists,
   the file is already on disk. A model that balks at the word "annas" protects
   nobody — the fetch has happened — and the only thing it can still do is refuse
   to hand over a file the user asked for, which breaks the session and leaves
   the download sitting there anyway.
3. **The model cannot judge it anyway.** Whether a given request is licensed
   turns on which sources the operator enabled, and on what credentials,
   subscriptions, institutional access or memberships back them. That is
   configuration the caller never sees. A licensing verdict read off a bare
   source name is a guess about a deployment the model was never shown — and it
   is the guess that produced the refusals this change was measured against.

The mirror host fails the rule for a simpler reason: it was never in the call in
any form, at any point.

The account allowance fails it conditionally. A call that sets
`annas_member: true` has asked for the member tier, so its quota answers a
question the caller actually put. A call that did not, against a server the
operator configured a key on, gets the file over that membership without ever
naming it — and reporting "27 of 50 downloads left" then discloses both that the
operator holds a paid account and how much of the window the user has spent.

## Decision

The download result carries no `source` and no `mirror`. Nothing is substituted
for them:

- **The serving source is not exposed at all**, not even indirectly. A caller
  that pinned nothing named no provider, so there is nothing to report back to
  it. A caller that pinned one needs nothing reported: pinning narrows the chain
  to that source alone — `selectSources` returns exactly the named source, and
  there is no substitution or fallback behind a pin — so a returned file came
  from the source the caller named, and an error means that source could not
  serve the item. The routing fact is in the outcome, not in a field.
- **`account`** is reported only when the call set `annas_member: true`.

### The boolean that was tried first

This decision originally shipped a three-state `served_by_requested_source`:
absent when the call pinned nothing, `true` when the pin held, `false` when a
different source served the file. The false state does not exist. Because a pin
is the whole chain, a pinned download either returns that source's file or fails
outright, so the flag was `true` whenever it was present and absent otherwise —
the same bit as "did I get a file", carried twice. It was withdrawn.

It is recorded here rather than quietly deleted because the flag reads as
reasonable until someone checks `selectSources`, and because its jsonschema
description told models what to do "when a different one did" serve the file,
documenting a behavior the code never implemented. Anyone reaching for a
provenance bit again should find this paragraph before rediscovering it.

The case that motivated the flag survives its removal. A model that pins
`archive`, is refused, and then pins a different source knows perfectly well
where the file it finally gets came from — it chose the source itself.

The source and the mirror stay inside the server, tagged out of the wire
(`json:"-"`). Cooldown bookkeeping needs both, and the `source resolved` log line
now carries the mirror as well — dropping it from the output would otherwise have
erased the fact rather than moved it. Provenance is the **operator's**
observability, and the server log is where an operator debugging a download
reads it.

The tool's own description was reworded to match: it leads with the chain's order
(openly licensed and open-access sources first, shadow-library mirrors only when
none of them serves the item), keeps the identifier-to-name mapping so a caller
can read the `source` enum and the `resolve_only` link, and states plainly that
the serving source is chosen while resolving and is not named in the result.
Where it introduces the `source` argument it also states the pin's contract —
one provider instead of all of them, with no substitution, so a file that comes
back came from it and a failure means it could not serve the item. That sentence
is what makes the argument usable without a provenance field behind it, and it
discloses nothing, since the caller supplied the name.

## What stays in the result on purpose

- **The saved `path`.** It names the real file, which the caller must open to
  find its download. Falsifying a path is not privacy, it is a broken tool. The
  path no longer carries mirror marks either, because the naming rule strips
  them — that is a naming decision, not a disclosure one.
- **`resolved.source` on the `resolve_only` path.** There the result hands back
  a direct URL, which identifies the provider by its own hostname. Withholding
  the name beside it would conceal nothing and only make the link harder to use.
- **`original_filename`.** On an unverified download it is the evidence of what
  actually arrived, and it is a property of the delivered file rather than of the
  deployment.

## Consequences

- Callers can no longer attribute a completed download to a provider. That is
  intended. Attribution before the call — which providers a deployment can reach
  — is still available from the `source` enum and the tool description; only the
  after-the-fact naming is gone.
- A caller that wants a specific provider must pin it, and the pin reports
  itself: the file that comes back is that provider's, or no file comes back.
  Pinning is the only way to learn anything about routing from a call, and what
  it learns comes from the outcome rather than from a field in the result.
- **The evaluator had to move its assertions to the server log.** Several
  scenarios graded the chain by reading `DownloadResult.Source` out of the
  result; they now parse the `source resolved` line out of `calls[].server_logs`,
  where the cooldown scenario already read. Re-grading the recorded run against
  both implementations returned byte-identical verdicts, and the log is the more
  faithful observation anyway — it watches what the server did rather than what
  the model was shown. The cost is real: log-derived evidence is coupled to a log
  message's wording, and a rename there silently stops grading.
- Operator-facing docs must point at the log rather than the result for any
  provenance question, and the log line is now load-bearing rather than
  incidental.
- The rule generalizes. Any future field that would report a deployment fact the
  call did not name — a credential's presence, an institutional entitlement, a
  chosen mirror — is refused by the same reasoning, or made conditional on the
  caller having asked, the way `account` is.

## When to revisit

If a deployment ever needs per-call provenance in the *result* — an audited
institutional gateway, say, where the operator and the caller are the same party
and the transcript stays inside their boundary — this becomes an opt-in the
**operator** enables, never a default and never something a tool argument can
switch on. A model asking for provenance is not the party whose disclosure it is.
