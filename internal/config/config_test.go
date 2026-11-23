package config

import "testing"

func TestEnvStripsByteOrderMarkAndWhitespace(t *testing.T) {
	t.Setenv("PLAYRADAR_TEST_KEY", "\uFEFFABC123\r\n")
	if got := env("PLAYRADAR_TEST_KEY", "fallback"); got != "ABC123" {
		t.Fatalf("env = %q", got)
	}
	t.Setenv("PLAYRADAR_TEST_KEY", "\uFEFF ")
	if got := env("PLAYRADAR_TEST_KEY", "fallback"); got != "fallback" {
		t.Fatalf("blank value = %q, want fallback", got)
	}
}
