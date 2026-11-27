# deploy/genesis

`job.yaml`: the `continuum-genesis` Job. It runs `cmd/continuum-genesis` to
render the devnet genesis into the `continuum-genesis` volume, with the seed
read from the `continuum-seed` Secret. Rerunning against the same volume and
spec is a no-op. Pool host names are fully qualified with the Job's namespace
through `$(POD_NAMESPACE)`. `hack/up.sh` and `test/e2e` apply it after
substituting the image and arguments.
