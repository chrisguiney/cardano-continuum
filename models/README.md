# models

TLA+ models and the tooling to check them.

- [`specs/`](specs/README.md): one directory per model.
- [`bin/`](bin/README.md): wrappers for the TLA+ tools.
- [`scripts/`](scripts/README.md): the tool installer and the model runner.

`make models-install` installs a pinned Java and TLA+ tools into
`models/.tools` (gitignored); `make models` checks every directory under
`specs/`.
