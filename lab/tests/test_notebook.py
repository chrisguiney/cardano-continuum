"""Runs the notebooks top to bottom so they cannot rot.

By default they read the small fixtures (and the shipped profiles). Point the
guide at real data with GUIDE_REAL=1 and GUIDE_CHAIN / GUIDE_EXPORT, or `pytest
--guide-real` with those set.
"""

import os

import nbclient
import nbformat
import pytest

from helpers import LAB

NOTEBOOKS = ["guide.ipynb", "explore.ipynb"]

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


def load(name):
    return nbformat.read(os.path.join(LAB, name), as_version=4)


def run_notebook(name, env):
    """Execute a notebook with the given environment variables set, and
    restore the environment afterwards."""
    notebook = load(name)
    notebook.cells.insert(0, nbformat.v4.new_code_cell(DIRECT_INTERACT))
    before = {key: os.environ.get(key) for key in env}
    os.environ.update(env)
    try:
        client = nbclient.NotebookClient(notebook, timeout=600, kernel_name="python3",
                                         resources={"metadata": {"path": LAB}})
        client.execute()
    finally:
        for key, value in before.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
    return notebook


def run_guide(env):
    return run_notebook("guide.ipynb", env)


def text_of(notebook):
    """Everything the notebook printed, one entry per output (an output
    that is not printed text, such as a figure, contributes an empty line)."""
    printed = []
    for cell in notebook.cells:
        for output in cell.get("outputs", []):
            printed.append(output.get("text", "") if output.output_type == "stream" else "")
    return "\n".join(printed)


def images(notebook):
    """How many figures the notebook drew."""
    return sum("image/png" in output.get("data", {}) for cell in notebook.cells for output in cell.get("outputs", []))


@pytest.mark.parametrize("name", NOTEBOOKS)
def test_committed_notebooks_have_no_outputs(name):
    """Outputs make diffs unreadable and can leak local paths; notebooks are
    committed cleared."""
    for cell in load(name).cells:
        if cell.cell_type == "code":
            assert not cell.outputs and cell.execution_count is None, f"{name}: outputs not cleared"


def test_guide_runs_on_fixtures_and_shows_the_mainnet_placeholder():
    """Every cell of the guide executes on the fixtures and draws its
    figures; mainnet shows as not fitted exactly when it has no shipped
    profile."""
    notebook = run_guide({"GUIDE_REAL": "0"})
    printed = text_of(notebook)
    assert "test fixtures" in printed
    assert "preview: " in printed and "preprod: " in printed
    assert images(notebook) >= 14
    mainnet_fitted = os.path.exists(os.path.join(LAB, "..", "profiles", "mainnet.yaml"))
    assert ("mainnet: not fitted yet" in printed) != mainnet_fitted


def test_explore_runs_the_fit_and_grade_loop_on_fixtures():
    """The calibration notebook fits the fixture chain, prints a profile
    and draws its two figures."""
    notebook = run_notebook("explore.ipynb", {"GUIDE_REAL": "0"})
    printed = text_of(notebook)
    assert "test fixtures" in printed
    assert "profiles:" in printed
    assert images(notebook) >= 2


def test_guide_runs_on_real_data_when_asked(request):
    if not request.config.getoption("--guide-real"):
        pytest.skip("pass --guide-real with GUIDE_CHAIN and GUIDE_EXPORT set")
    notebook = run_guide({"GUIDE_REAL": "1"})
    assert "REAL preprod chain" in text_of(notebook)
