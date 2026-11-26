# cardano-continuum

A stochastic, adversarial Cardano population running on a live devnet.

cardano-continuum starts a devnet from a blank genesis and keeps it running. A
population of simulated wallets, pools, DReps and contracts pays, delegates,
votes and deploys with randomised timing, and retires or goes dormant over
time. Some actors misbehave on purpose. The devnet is the test subject: the
project is a stress, conformance and differential test harness for Cardano
node implementations.

## Components

| Name | Role |
| --- | --- |
| `continuum-engine` | Runs the simulated population against a devnet |
| `continuum-operator` | Kubernetes operator that deploys networks and pools |
| `continuum-forger` | Block producer that forges blocks on purpose to misbehave |
| `continuum-genesis` | Genesis Job that writes a devnet's genesis files |
| `continuum-lab` | Notebooks and scripts for analysing run output |
| `continuum` | Command-line interface over the components above |

## Development

Requires Go 1.27 (see `go.mod`) and `make`. Run the full local check, which is
the same set of steps CI runs on push to `main`:

```sh
make check
```

`make check` builds, vets, checks formatting, runs the tests with the race
detector and the `debug` build tag, and runs `golangci-lint`. Install
`golangci-lint` v2.15.0 first; CI uses that version.

Simulate a wallet population paying each other on the in-process fake chain,
then replay the log and check that every decision is reproduced:

```sh
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --epochs 2 --out run1
go run ./cmd/continuum replay --seed 7 --wallets 100 run1/events.log
```

`replay` takes the same seed and population flags as `run`; the log header
refuses a log recorded under different ones. `make determinism` runs the
determinism tests under the race detector at `GOMAXPROCS=1` and `8`.

## Web UI

`continuum serve --log run1/events.log` serves the web UI and its API on
`127.0.0.1:8088`. The single-page app (Preact, Vite, TypeScript) is in `web/`;
its build, `web/dist`, is committed and embedded in the binary, so Go builds
need no Node. After changing `web/src`, run `make web` and commit `web/dist`;
`make web-test` typechecks and runs the unit tests. Screenshots are in
`docs/screenshots/`; the `-sample` ones use stand-in API data because a fake
run has no pools and writes its log only when it ends.

TLA+ models live under `models/`. Install the pinned tools once, then check
every model:

```sh
make models-install
make models
```

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
