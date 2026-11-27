# models/scripts

- `install-tla.sh`: installs a private Java and the TLA+ tools into
  `models/.tools`, each download pinned by version and SHA-256. The pinned
  JDK is used wherever one exists for the platform (Linux x86-64); elsewhere
  the system Java is the fallback, and every run says which Java it used. Run
  by `make models-install`.
- `run-model.sh specs/<dir> [Config.cfg ...]`: checks the configs of a model
  directory (all of them, or the ones named). A config named `*Bug.cfg` must
  fail on the one property it names (its single `INVARIANT`/`PROPERTY` entry,
  or a `\* EXPECT: Name` line; `\* EXPECT: Deadlock` for a deadlock); every
  other config must pass. Each verdict, failed property and passing distinct
  state count is compared with the directory's committed `verdicts.tsv`;
  `TLC_RECORD=1` rewrites it from the run instead. Runs take a per-user lock
  (`TLC_LOCK`, waiting up to `TLC_LOCK_WAIT` seconds), so only one TLC runs
  at a time, and each run has its own `java.io.tmpdir` and `-metadir`. Run
  for each directory by `make models`. `TLC_OUT=<dir>` keeps each config's
  TLC output (and so its trace) as `<dir>/<Config>.out`.
- `changed-models.sh BASE`: prints the model directories a change since
  `BASE` can affect: the specs it touched, the specs whose README or `.tla`
  names a Go file or package it touched, or every spec when the runner, the
  installer or `bin/` changed. `make models-changed BASE=origin/main` checks
  only those.
- `drift.sh [REV]`: for each model, the code it maps that changed between
  the commit its README pins (`Models cardano-continuum at <sha>`) and
  `REV`. Each listed file is a model to re-read against the code and
  re-pin. `make models-drift`.
