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

TLA+ models live under `models/`. Install the pinned tools once, then check
every model:

```sh
make models-install
make models
```

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
