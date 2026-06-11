"""Unit tests for pure Seidr helpers — no DB, network, or inference required."""

import main


def test_sanitize_agent_name_normalizes():
    assert main.sanitize_agent_name("inference-host-01") == "frey_01"
    assert main.sanitize_agent_name("  Ragnar!! ") == "ragnar"
    assert main.sanitize_agent_name("a/b//c") == "a_b_c"


def test_sanitize_agent_name_pads_short_and_truncates_long():
    # Names shorter than 3 chars are right-padded with underscores.
    assert main.sanitize_agent_name("") == "___"
    assert main.sanitize_agent_name("AB") == "ab_"
    # Names longer than 50 chars are truncated.
    assert len(main.sanitize_agent_name("x" * 80)) == 50


def test_timestamp_round_trip():
    ts = main.format_timestamp()
    parsed = main.parse_timestamp(ts)
    assert parsed is not None
    assert main.format_timestamp(parsed) == ts


def test_parse_timestamp_rejects_garbage():
    assert main.parse_timestamp("not-a-timestamp") is None
    assert main.parse_timestamp("") is None


def test_safe_metadata_handles_dict_json_and_invalid():
    assert main.safe_metadata({"a": 1}) == {"a": 1}
    assert main.safe_metadata('{"a": 1}') == {"a": 1}
    assert main.safe_metadata("not json") == {}
    assert main.safe_metadata(None) == {}
    # A JSON array is not a dict, so it is rejected.
    assert main.safe_metadata("[1, 2, 3]") == {}


def test_memory_ttl_days_for_type():
    assert main.memory_ttl_days_for_type("lesson") == 90
    assert main.memory_ttl_days_for_type("workflow") == 90
    assert main.memory_ttl_days_for_type("general") == 10
    assert main.memory_ttl_days_for_type("soul_candidate") is None
    assert main.memory_ttl_days_for_type("skill_amendment") is None


def test_normalize_layer():
    assert main.normalize_layer("lesson") == "lesson"
    assert main.normalize_layer("EXPERIENCE") == "experience"
    assert main.normalize_layer("bogus") == "experience"
    assert main.normalize_layer(None) == "experience"


def test_normalize_confidence_defaults_and_clamps():
    assert main.normalize_confidence(None, "lesson") == 0.7
    assert main.normalize_confidence(None, "experience") == 0.5
    assert main.normalize_confidence(2.0, "lesson") == 1.0
    assert main.normalize_confidence(-1.0, "experience") == 0.0
    assert main.normalize_confidence(0.42, "experience") == 0.42


def test_sha256_hex_is_stable():
    assert main.sha256_hex("valhalla") == main.sha256_hex("valhalla")
    assert main.sha256_hex("a") != main.sha256_hex("b")
    assert len(main.sha256_hex("x")) == 64


def test_audit_chain_hash_is_deterministic_and_sensitive():
    args = ("genesis", "inference-host", "store", "m1", "contenthash", "2026-01-01T00:00:00Z")
    first = main.compute_chain_hash_from_content_hash(*args)
    assert first == main.compute_chain_hash_from_content_hash(*args)
    # Changing any field changes the chain hash.
    changed = main.compute_chain_hash_from_content_hash("genesis", "inference-host", "delete", "m1", "contenthash", "2026-01-01T00:00:00Z")
    assert changed != first
