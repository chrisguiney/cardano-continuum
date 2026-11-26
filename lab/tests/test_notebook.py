"""Runs the guide notebook top to bottom so it cannot rot.

By default it reads the small fixtures (and the shipped profiles). Point it at
real data with GUIDE_REAL=1 and GUIDE_CHAIN / GUIDE_EXPORT, or `pytest
--guide-real` with those set.
"""

import json
import os

import nbclient
import nbformat
import pytest

LAB = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NOTEBOOKS = ["guide.ipynb", "explore.ipynb"]


def load(name):
    return nbformat.read(os.path.join(LAB, name), as_version=4)


@pytest.mark.parametrize("name", NOTEBOOKS)
def test_committed_notebooks_have_no_outputs(name):
    for cell in load(name).cells:
        if cell.cell_type == "code":
            assert not cell.outputs and cell.execution_count is None, f"{name}: outputs not cleared"


# interact() draws inside widget output areas, where an exception never
# reaches the cell. The test swaps it for a direct call at the functions'
# default arguments, so a broken callback fails the run.
DIRECT_INTERACT = """
import ipywidgets
def _interact(f=None, **kw):
    if callable(f):
        f()
        return f
    return lambda g: (g(), g)[1]
ipywidgets.interact = _interact
"""


def run_guide(env):
    nb = load("guide.ipynb")
    nb.cells.insert(0, nbformat.v4.new_code_cell(DIRECT_INTERACT))
    old = {k: os.environ.get(k) for k in env}
    os.environ.update(env)
    try:
        nbclient.NotebookClient(nb, timeout=600, kernel_name="python3", resources={"metadata": {"path": LAB}}).execute()
    finally:
        for k, v in old.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
    return nb


def text_of(nb):
    out = []
    for c in nb.cells:
        for o in c.get("outputs", []):
            out.append(o.get("text", "") if o.output_type == "stream" else json.dumps(o.get("data", {}))[:0])
    return "\n".join(out)


def images(nb):
    return sum("image/png" in o.get("data", {}) for c in nb.cells for o in c.get("outputs", []))


def test_guide_runs_on_fixtures_and_shows_the_mainnet_placeholder():
    nb = run_guide({"GUIDE_REAL": "0"})
    out = text_of(nb)
    assert "test fixtures" in out
    assert "preview: " in out and "preprod: " in out
    assert images(nb) >= 14
    mainnet_fitted = os.path.exists(os.path.join(LAB, "..", "profiles", "mainnet.yaml"))
    assert ("mainnet: not fitted yet" in out) != mainnet_fitted


def test_guide_runs_on_real_data_when_asked(request):
    if not request.config.getoption("--guide-real"):
        pytest.skip("pass --guide-real with GUIDE_CHAIN and GUIDE_EXPORT set")
    nb = run_guide({"GUIDE_REAL": "1"})
    assert "REAL preprod chain" in text_of(nb)
