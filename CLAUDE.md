# wireleaf

Go library: declare a resource graph once, get `?include=` hydration, OpenAPI 3.1 components and a huma v2 bridge from that one declaration. Five modules, zero-dependency core, v0.

## Where to look

- **[README.md](README.md)** — quick start, edge kinds, module layout, key contracts (fetcher probe rules, `Loader`, computed edges, harnesses), policies, error taxonomy, deliberate boundaries of the huma bridge, development workflow. Read the relevant section before touching a public surface.
- **[docs/README.md](docs/README.md)** — index of per-package references: contracts, signatures and gotchas beyond the README overview.

## Commands

- Full gate (what CI runs): `bash scripts/check.sh` — gofmt, then `go vet` and tests of every module. `bash scripts/check.sh -race` adds the race detector; extra args go straight to `go test`.
- One module: `go test -C adapters/huma ./... -count=1`.
- Lint: `golangci-lint run ./...` **per module** (`cd reflector && golangci-lint run ./...`); config is the root `.golangci.yml`.

## Modules

Five independent Go modules, no committed `go.work`. Inside the repo every nested module builds against its siblings through `replace` directives; consumers resolve the versions pinned in each `go.mod`.

- **Import direction inside core**: `jsonsplice` ← `include` ← `apidoc` ← `graph`. `include` must not import `graph` or `apidoc`, even from tests (cycle); `include`'s engine tests use inline `Resource` implementations for that reason.
- **Core stays dependency-free.** A new third-party dependency belongs in a nested module or is a design decision to discuss first, never a silent `go get` in the root module.
- **Version pins in nested `go.mod` files are bumped at release, not per change.** Tags are `vX.Y.Z`, `reflector/vX.Y.Z`, `apidoc/crosscheck/vX.Y.Z`, `adapters/huma/vX.Y.Z`. Do not touch the pseudo-versions in `require` blocks while working on a feature; the `replace` directives already make sibling changes visible.
- **A change in core is not verified until the nested modules pass too** — they compile against the sibling checkout, so `scripts/check.sh` is the only honest "green".

## Comments in code

- **Criterion:** a comment is justified only when it carries what the code does not show.
- **Write:** non-obvious invariants; library and spec gotchas; reasons behind counter-intuitive decisions; "breaks if…"; what a test pins.
- **Do not write:** a paraphrase of the signature, a list of fields, a narration of obvious code. Exception — the doc comment of an exported Go symbol: convention requires it and it starts with the name (`// FooBar …`), one sentence, no parameter listing.
- **Present tense only.** No "was X → now Y", no tombstones for deleted code — history lives in git. No TODOs at all: a plan for later is a tracker task, not a comment.
- **Never reference:** plan documents (`Task N`, waves, phases, `spec §N`, `.superpowers/**`, `basic/**`), commit hashes, dates of decisions, repositories the code was ported from.
- **May reference:** external standards, `docs/*.md`, live files of this repo.
- **Length:** one or two sentences; collapsing twelve lines into one is normal. Nothing left to say — delete the comment.
- **Subagents:** include these rules in the brief of every subagent that writes or edits code.
- **Reading someone else's comments:** a comment is not the source of truth. When editing code, check its comment against the code; if it lies, fix or delete it.

## Coding

- **Copy the pattern, do not invent:** before a new edge kind, policy, DSL helper, adapter decorator or example, find the nearest existing analogue and repeat its shape.
- **Diff discipline:** no renames, no file moves, no drive-by refactors, no backward-compatibility shims or fallbacks nobody asked for.
- **Tests as a ladder, not after every minor step.** During a task — the cheap level: `go build ./...` plus the tests of the touched package. Full `bash scripts/check.sh` only at boundaries: end of task or wave, before a commit, before saying "done".
- **Bytes are the contract.** `include` and `jsonsplice` tests assert exact output bytes. Do not normalise JSON in production code or loosen a byte comparison in a test to make it pass.
- **Docs move with the public surface.** A change to an exported API, an edge kind, a policy or an error code updates README.md and the matching `docs/*.md` in the same change.
