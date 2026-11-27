# .github/workflows

`ci.yml` is the one workflow. It runs on pushes to `main` and on manual
dispatch.

| Job | Runs on | Does |
|---|---|---|
| `static` | push | `make check-static`: build, vet, gofmt, golangci-lint, the generated-file drift check and the Helm chart lint and render (`make chart-check`) |
| `test` | push | `make test-race SHARD=...`, one job per shard (`engine-a`, `engine-b`, `engine-internal`, `cli`, `rest`); together they run every test once |
| `web-test` | push | `make web-test`: typecheck and Vitest for [`web/`](../../web/README.md) (Node 22) |

No job runs the end-to-end suite: GitHub's runners have no cluster. It runs
against a cluster you provide, with `make e2e E2E_CONTEXT=<context>
CONTINUUM_REGISTRY=<registry>` (see the [README](../../README.md#end-to-end-tests)).

`static` and `test` together are `make check`, the local entry point. Actions
are pinned by commit SHA; the `static` job reads the golangci-lint version from
the Makefile's `GOLANGCI_LINT_VERSION`. Each Go job restores and saves its own
module and build cache, saving even when the job fails.
