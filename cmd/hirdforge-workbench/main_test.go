package main

import "testing"

func TestResolveAddrDefault(t *testing.T) {
	t.Setenv("HIRDFORGE_WORKBENCH_HOST", "")
	t.Setenv("HIRDFORGE_WORKBENCH_PORT", "")
	if got := resolveAddr(); got != "127.0.0.1:7777" {
		t.Fatalf("expected default addr 127.0.0.1:7777, got %q", got)
	}
}

func TestResolveAddrEnvOverride(t *testing.T) {
	t.Setenv("HIRDFORGE_WORKBENCH_HOST", "0.0.0.0")
	t.Setenv("HIRDFORGE_WORKBENCH_PORT", "9999")
	if got := resolveAddr(); got != "0.0.0.0:9999" {
		t.Fatalf("expected overridden addr 0.0.0.0:9999, got %q", got)
	}
}
