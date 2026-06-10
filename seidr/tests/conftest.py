"""Shared pytest fixtures for the Seidr smoke tests.

Importing seidr/main.py has no side effects (the DB pool and embedding model are
created only under `__main__`), so these tests run with no DB, network, GPU, or
inference backend.
"""

import os
import sys

# Make the seidr/ service root importable (main.py, mcp_server.py) regardless of
# the working directory pytest is invoked from.
SEIDR_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if SEIDR_ROOT not in sys.path:
    sys.path.insert(0, SEIDR_ROOT)

import pytest  # noqa: E402  (import after sys.path setup is intentional)
from fastapi.testclient import TestClient  # noqa: E402

import main  # noqa: E402


@pytest.fixture(scope="session")
def app_module():
    """The imported Seidr main module."""
    return main


@pytest.fixture(scope="session")
def client():
    """A FastAPI TestClient bound to the Seidr app (no DB pool initialized)."""
    return TestClient(main.app)
