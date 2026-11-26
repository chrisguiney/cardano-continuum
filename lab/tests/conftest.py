"""Shared setup: the lab modules are plain files in the parent directory,
imported by name, so that directory goes on the import path."""

import os
import sys

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, HERE)


def pytest_addoption(parser):
    parser.addoption("--guide-real", action="store_true", help="run the guide notebook on real data (GUIDE_CHAIN, GUIDE_EXPORT)")


@pytest.fixture(scope="session")
def chain():
    """Statistics of the chain fixture: 100 preview transactions."""
    import chain_stats
    from helpers import CHAIN

    return chain_stats.chain_stats(CHAIN)


@pytest.fixture(scope="session")
def engine():
    """Statistics of the engine fixture: the first 200 payments of a seed-7 fake run."""
    import engine_stats
    from helpers import ENGINE

    return engine_stats.engine_stats(ENGINE)
