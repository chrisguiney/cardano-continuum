# deploy/engine

- `statefulset.yaml`: runs `continuum run --node` against a live devnet, with
  its event log and SQLite store on a PVC so a restarted pod resumes. One
  replica only. The header comment lists the Secret and ConfigMaps it reads.
- `rbac.yaml`: ServiceAccount, Role and RoleBinding letting the engine patch
  `ContinuumPool`s and read `ContinuumNetwork`s.

`hack/up.sh` applies both, with substitutions, and `test/e2e` applies them in
the payments test. The image is `./cmd/continuum`, built by `make ko-images`.
