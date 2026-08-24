package auth

import "testing"

func TestGetTokenWithSourcePrefersExplicitEnvironment(t *testing.T) {
	t.Setenv("BUILDER_GITHUB_TOKEN", "test-builder-token")
	t.Setenv("GH_TOKEN", "test-gh-token")
	t.Setenv("GITHUB_TOKEN", "test-actions-token")
	token, source, err := GetTokenWithSource()
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-builder-token" {
		t.Fatalf("token = %q", token)
	}
	if source != "environment (BUILDER_GITHUB_TOKEN)" {
		t.Fatalf("source = %q", source)
	}
}
