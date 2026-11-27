# config/crd/bases

Generated CustomResourceDefinitions for the `continuum.blinklabs.io` API group:

- `continuum.blinklabs.io_continuumnetworks.yaml`: `ContinuumNetwork`.
- `continuum.blinklabs.io_continuumpools.yaml`: `ContinuumPool`.

Both come from the types and markers in `api/`. `make manifests` writes them
with controller-gen (it only writes the CRDs it generates and deletes nothing,
so every file in this directory is generated; change the Go types, not the
YAML). `make check-manifests` fails on drift or on an untracked generated file.
