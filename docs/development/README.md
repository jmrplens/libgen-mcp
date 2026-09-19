# Developer documentation

For people changing this repository. The pages under `docs/` one level up are
for people *using* the server; these are about the machinery around it.

This tree is **repository-only**. It is not mirrored to the docs site and it is
not translated: the site publishes what a user needs, and a bilingual copy of a
contributor page is a second thing to keep true for no reader who was going to
find it there. The site's own parity and link checks are not widened to cover
it.

## The pages

| Page                     | Read it when                                                                                                                |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| [gates.md](gates.md)     | A check went red and you want to know what it was asserting, where it runs, and where the rule it enforces is written down. |
| [testing.md](testing.md) | You are deciding which of the five test surfaces a change needs.                                                            |

## What is not here

- **The conventions themselves.** `CLAUDE.md` at the repository root is the one
  place they live: how a tool is registered, why the served surface is ASCII,
  what may appear in a URL, which namespace a telemetry name goes in. This tree
  points at it rather than restating it, because a rule written twice is a rule
  that will disagree with itself.
- **Architecture decisions.** `docs/decisions/` holds the ADRs.
- **The release sequence.** It is a skill, `.claude/skills/release/SKILL.md`,
  because it is a procedure to follow rather than a page to read. The three
  rules from it that have each already cost a publish are repeated in
  `CLAUDE.md` § *Release Process*, because a rule that lives only in a skill is
  a rule an agent has to invoke something to see.
- **`docs/superpowers/`.** A historical tree of plans and specifications, kept
  for the record and superseded by design. It is not maintained and not held to
  the documentation gates.

## The short version

```sh
make help          # every target, with what it does
make analyze       # the pre-commit sweep: format, lint, vet, and the doc gates
make test          # the unit suite
```

`make analyze` is a **deliberate action before a commit**, not something to run
on every save: it runs the linter under every build tag and several generators
in check mode, which takes long enough to be annoying in a file watcher and
long enough to be worth it once.
