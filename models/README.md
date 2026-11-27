# models

TLA+ models and the tooling to check them.

- [`specs/`](specs/README.md): one directory per model.
- [`bin/`](bin/README.md): wrappers for the TLA+ tools.
- [`scripts/`](scripts/README.md): the tool installer and the model runner.

`make models-install` installs a pinned Java and TLA+ tools into
`models/.tools` (gitignored); `make models` checks every directory under
`specs/`, and `make models-changed BASE=origin/main` checks only the ones a
change since `BASE` can affect (every one if the runner or the tools
changed). `make models-drift` lists the code each model maps that changed
since the commit it models. CI (`.github/workflows/models.yml`) runs
`models-changed` on every push to main and the full suite nightly.

## Running TLC

- One TLC run at a time: the runner takes a per-user lock, so a second
  `make models` waits for the first. Never run model targets with `make -j`.
- The heap is capped at `TLC_HEAP` (default `6g`), and each run gets its own
  `java.io.tmpdir` and `-metadir`.
- Check one directory while working (`scripts/run-model.sh specs/<dir>`, or
  name the configs to run); the full suite is for merges.

## Verdicts

A config's expected outcome is in its name: `*Bug.cfg` must fail, and only on
the property it names; every other config must pass. Each directory's
`verdicts.tsv` records the verdict, the failed property and, for a passing
config, the distinct state count and time of the last recorded run. The runner
fails on any difference, so a change to a spec shows its effect on every
config: re-record with `TLC_RECORD=1 scripts/run-model.sh specs/<dir>` and
explain each changed row in the commit. An unexplained drop in a passing
config's distinct count is a lost behaviour until shown otherwise. Bug-config
state counts vary with the worker count and are not compared.

## Deadlock and liveness

TLC's deadlock check stays on. A model whose run ends at its bounds says so
with an explicit terminal, `Done`, enabled only in the states the model means
to end in; any other state with no successor is a real stuck state and fails
the run. A config that turns the check off says why in the config, and a
liveness property covers what the check would have. Liveness lives in its own
`*Live.cfg`, with the fairness it needs stated in the spec.

## Defects: model first

A suspected defect, or a review finding on a fix, goes into the model before
any Go changes, in this order, each its own commit or at least visible in
order in one:

1. a `<Name>Bug.cfg` modelling the code as it is, failing on the property the
   defect breaks;
2. a `<Name>Fixed.cfg` (or the passing config, once the rule changes)
   modelling the proposed fix, at the Bug config's bounds, passing;
3. a Go test that fails for the reason TLC printed;
4. the fix.

A passing config never leaves out an invariant it is known to violate; such a
config is a Bug config. When the fix lands and the model's rules change to
it, the old behaviour stays as a mutation, so its Bug config keeps the
invariant's teeth.

## Traceability

Each model's README pins the commit it models
(``Models cardano-continuum at `<sha>` ``), maps every action to the
`file:line` it stands for, and lists in a drift table where the model
deliberately differs from the code. When `make models-drift` lists a file,
re-read the model against the code and re-pin.
