# models/scripts

- `install-tla.sh`: installs a private Java and the TLA+ tools into
  `models/.tools`, each download pinned by version and SHA-256. Run by
  `make models-install`.
- `run-model.sh specs/<dir>`: checks every config in a model directory. A
  config named `*Bug.cfg` must report a violation or deadlock; every other
  config must pass. Run for each directory by `make models`.
