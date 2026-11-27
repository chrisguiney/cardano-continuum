# lab/tests

pytest suite for the lab modules; one `test_<module>.py` per module (fit,
grade, estimators, cluster, units, agendas, actor fit and grade, stats,
guide-data export, Hawkes parameters). `test_notebook.py` runs the notebooks
top to bottom. `conftest.py` puts the parent directory on the import path;
`helpers.py` holds fixture paths and synthetic inputs.

Run with `make lab-test` (inside the lab image, then `go test ./lab/`). The
data is in [`fixtures/`](fixtures/README.md).
