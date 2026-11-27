# .github/workflows

`ci.yml` is the one workflow. It runs on pushes to `main`, nightly (03:17 UTC)
and on manual dispatch.

| Job | Runs on | Does |
|---|---|---|
| `check` | push | `make check`: build, vet, gofmt, race tests, golangci-lint and the generated-file drift check |
| `web-test` | push | `make web-test`: typecheck and Vitest for [`web/`](../../web/README.md) (Node 22) |
| `e2e` | nightly, dispatch | `make e2e` on a kind cluster, then `make kind-down` (see [`hack/`](../../hack/README.md)) |

Actions are pinned by commit SHA; golangci-lint is pinned to the version in the
Makefile.
