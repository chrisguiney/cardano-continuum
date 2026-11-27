# deploy/lab

A kustomization (`kustomization.yaml`) of `deployment.yaml`: the
`continuum-lab` Deployment, JupyterLab with DuckDB and pandas over a run's
exported Parquet tables. The data volume is read-only; the optional
`continuum-lab` Secret (key `token`) supplies the Jupyter token. The image is
built from [`lab/Dockerfile`](../../lab/README.md). `make up` does not apply
this directory.
