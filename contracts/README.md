# Contracts

Aiken validators the engine deploys and calls. The compiled blueprint
(`templates/plutus.json`, CIP-57) and the Go registry generated from it
(`internal/contract/registry_gen.go`) are committed, as `web/dist` is, so
`go build` and `make check` need no Aiken.

## Validators

| Validator | Purpose | What it checks | Engine use |
| --- | --- | --- | --- |
| `always_succeeds` | spend | nothing | contract 0, the shared lock adversaries race for |
| `vesting(nonce)` | spend | the datum's owner signs, and the validity range starts at or after `lock_until` | odd contracts (1, 3, ...) |
| `escrow(nonce)` | spend | the depositor signs; Release: an output pays the beneficiary at least the escrowed lovelace. Refund: the validity range starts at or after the deadline | even contracts (2, 4, ...): a script user locks for another script user, who submits the release with the depositor's signature; a disputed escrow (one in four) is refunded from its deadline |
| `disbursement(nonce)` | spend | the beneficiary and the administrator sign, the validity range starts at or after `next`, one award is spent, and unless at most one `tranche` is left an output pays the rest back with `next` moved on by `period` | the one instance `build.Disbursement`: a treasury recipient that draws in milestones withdraws its award straight into it, and draws a tranche each `period` with the governance administrator's signature |
| `free_mint` | mint | nothing | every `mint` and `burn` |

Datum and redeemer types are in `templates/lib/continuum/types.ak`; their
Go types are generated from the blueprint's definitions into
`internal/contract/types_gen.go` (records and enums only; any other shape
fails `make contracts`). `nonce` does nothing on
chain: applying a different one gives each deployed contract its own script
hash and address (`contract.Apply`, `build.Contract`). Contracts 0 to 2
exist from genesis.

## Compilation phases in the UI

`/contracts/<name>` (and `/contracts/<name>/<n>` for the instance applied
to nonce `n`) shows a validator as Aiken source, UPLC text, flat bytes and
the on-chain bytes with the script hash derivation; the API is
`GET /api/contracts/<name>` and `/api/contracts/<name>/instances/<n>`.
Only the source is a snapshot: `internal/contract/sources_gen.go` holds the
`.ak` files under `templates/lib` and `templates/validators`. The other
phases are computed from the committed script by `contract.Pipe` (plutigo
decodes the flat program and prints it as UPLC), so a Go build still needs
no Aiken. Source lines are not mapped to UPLC terms: Aiken's source maps
are not part of the blueprint.

## Pins

`tools.lock` pins the compiler: Aiken v1.1.24 (commit `bacbeb35`), with the
SHA-256 of each release tarball. `templates/aiken.toml` pins the standard
library by commit (`dfdf5ffc`, tag v4.0.0, the version v1.1.24 scaffolds).
`scripts/install-aiken.sh` downloads the tarball for this platform (macOS
arm64, Linux x86_64 or arm64) into `.tools/aiken/`, refuses a checksum
mismatch, and checks the binary's version. An `aiken` on `PATH` is never
used.

`aiken check` runs the unit tests beside each validator and property tests
(`prop_*`) whose inputs come from `aiken-lang/fuzz`, pinned by commit in
`templates/aiken.toml`; validators never import it.

## Building

```sh
make contracts        # aiken check, aiken build --trace-level silent, go generate
make contracts-check  # the same, then fail if anything committed changed
```

The trace level is part of the script bytes and so of every hash and
address; builds are silent. `make contracts-check` is the reproducibility
check and runs in CI as its own job, since it needs the compiler.

Tests in `internal/contract` fail when `registry_gen.go` drifts from the
blueprint, when `sources_gen.go` drifts from the `.ak` files, when a blueprint hash differs from gouroboros' hash of the same
bytes, and when plutigo's flat codec does not round-trip a validator
(parameter application decodes and re-encodes it).

## Adding a validator

1. Write it in `templates/validators/<name>.ak`, with `aiken check` tests,
   and any shared types in `templates/lib/continuum/`.
2. Run `make contracts` and commit the `.ak` files, `plutus.json`,
   `registry_gen.go` and `sources_gen.go` together; the UI shows the new
   validator with no further change.
3. Use it through `contract.Named("<name>")`; for a builder script, as
   `build.AlwaysSucceeds` and `build.FreeMint` do, or as a template in
   `build.Template`. A new template also needs its datum, claim rule and
   redeemer in `internal/engine/internal/intent/script.go`.

Changing a validator, the compiler or the stdlib changes script hashes and
addresses, and so every seeded run's event log.
