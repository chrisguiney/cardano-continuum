# .github/workflows

`ci.yml` is the one workflow. It runs on pushes to `main`, nightly (03:17 UTC)
and on manual dispatch.

| Job | Runs on | Does |
|---|---|---|
| `static` | push | `make check-static`: build, vet, gofmt, golangci-lint and the generated-file drift check |
| `test` | push | `make test-race SHARD=...`, one job per shard (`engine-a`, `engine-b`, `engine-internal`, `cli`, `rest`); together they run every test once |
| `web-test` | push | `make web-test`: typecheck and Vitest for [`web/`](../../web/README.md) (Node 22) |
| `e2e` | nightly, dispatch | `make e2e` on a kind cluster, then `make kind-down` (see [`hack/`](../../hack/README.md)) |

`static` and `test` together are `make check`, the local entry point. Actions
are pinned by commit SHA; the `static` job reads the golangci-lint version from
the Makefile's `GOLANGCI_LINT_VERSION`. Each Go job restores and saves its own
module and build cache, saving even when the job fails.
