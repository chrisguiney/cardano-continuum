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
| `escrow(nonce)` | spend | Release: depositor signs and an output pays the beneficiary at least the escrowed lovelace. Refund: beneficiary signs | even contracts (2, 4, ...) |
| `free_mint` | mint | nothing | every `mint` and `burn` |

Datum and redeemer types are in `templates/lib/continuum/types.ak`; their
Go encoders are in `internal/contract/datum.go`. `nonce` does nothing on
chain: applying a different one gives each deployed contract its own script
hash and address (`contract.Apply`, `build.Contract`). Contracts 0 to 2
exist from genesis.

## Pins

`tools.lock` pins the compiler: Aiken v1.1.24 (commit `bacbeb35`), with the
SHA-256 of each release tarball. `templates/aiken.toml` pins the standard
library by commit (`dfdf5ffc`, tag v4.0.0, the version v1.1.24 scaffolds).
`scripts/install-aiken.sh` downloads the tarball for this platform (macOS
arm64, Linux x86_64 or arm64) into `.tools/aiken/`, refuses a checksum
mismatch, and checks the binary's version. An `aiken` on `PATH` is never
used.

## Building

```sh
make contracts        # aiken check, aiken build --trace-level silent, go generate
make contracts-check  # the same, then fail if anything committed changed
```

The trace level is part of the script bytes and so of every hash and
address; builds are silent. `make contracts-check` is the reproducibility
check and runs in CI as its own job, since it needs the compiler.

Tests in `internal/contract` fail when `registry_gen.go` drifts from the
blueprint, when a blueprint hash differs from gouroboros' hash of the same
bytes, and when plutigo's flat codec does not round-trip a validator
(parameter application decodes and re-encodes it).

## Adding a validator

1. Write it in `templates/validators/<name>.ak`, with `aiken check` tests,
   and any shared types in `templates/lib/continuum/`.
2. Run `make contracts` and commit the `.ak` files, `plutus.json` and
   `registry_gen.go` together.
3. Use it through `contract.Named("<name>")`; for a builder script, as
   `build.AlwaysSucceeds` and `build.FreeMint` do, or as a template in
   `build.Template`. A new template also needs its datum, claim rule and
   redeemer in `internal/engine/internal/intent/script.go`.

Changing a validator, the compiler or the stdlib changes script hashes and
addresses, and so every seeded run's event log.
