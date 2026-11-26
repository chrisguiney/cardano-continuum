# The lab image: the mithril2pq notebook image (JupyterLab, DuckDB, pandas,
# pyarrow, matplotlib) plus scipy for the fits and pytest for lab/tests, with
# this directory at /work/lab. Build from the repository root:
#   docker build -f lab/Dockerfile -t continuum-lab:local .
ARG BASE=mithril2pq-notebook:local
FROM ${BASE}
USER root
COPY lab/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt && rm /tmp/requirements.txt
COPY lab/ /work/lab/
RUN rm -rf /work/lab/__pycache__ /work/lab/tests/__pycache__ && chown -R jupyter /work/lab
WORKDIR /work
USER jupyter
