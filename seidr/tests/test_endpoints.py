"""Endpoint-level smoke tests via FastAPI TestClient.

These exercise routes that do not require a database, embedding model, or network.
The DB pool is never initialized, so DB-backed endpoints degrade gracefully — the
tests assert that contract rather than requiring a live database.
"""


def test_a2m_health(client):
    resp = client.get("/a2m/health")
    assert resp.status_code == 200
    body = resp.json()
    assert body["healthy"] is True
    assert body["provider"] == "seidr"
    assert body["a2m_version"] == "0.3"


def test_a2m_capability_card(client):
    resp = client.get("/.well-known/a2m.json")
    assert resp.status_code == 200
    body = resp.json()
    assert body["a2m"] == "0.3"
    assert body["name"] == "seidr"
    # Capability card advertises the core memory methods.
    assert "memory/store" in body["methods"]
    assert "memory/query" in body["methods"]


def test_cognitive_status(client):
    resp = client.get("/cognitive/status")
    assert resp.status_code == 200
    body = resp.json()
    # No inference URL configured in the test environment.
    assert body["enabled"] is False
    assert "stats" in body
    assert "total_facts_extracted" in body["stats"]


def test_health_degrades_without_db(client):
    # /health is wired and returns 200 even when the DB pool is absent; it reports
    # an error status rather than raising.
    resp = client.get("/health")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "error"


def test_collections_degrades_without_db(client):
    resp = client.get("/collections")
    assert resp.status_code == 200
    body = resp.json()
    assert "collections" in body
    assert body["collections"] == []
