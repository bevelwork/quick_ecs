package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestVersionFlag verifies the binary runs with --version without needing AWS creds.
func TestVersionFlag(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", "quick_ecs_testbin", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test binary: %v\n%s", err, string(out))
	}

	cmd = exec.Command("./quick_ecs_testbin", "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--version should succeed, got error: %v\n%s", err, string(out))
	}

	versionStr := strings.TrimSpace(string(out))
	if versionStr == "" {
		t.Fatalf("expected non-empty version output")
	}
}

// TestAuthFailureWithoutCreds ensures the app fails gracefully when no AWS creds are present.
// We explicitly clear AWS auth-related env vars and expect a non-zero exit.
func TestAuthFailureWithoutCreds(t *testing.T) {
	cmd := exec.Command("./quick_ecs_testbin")
	// Clear environment to remove AWS creds
	cmd.Env = []string{}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected authentication failure without AWS credentials; got success. output: %s", string(out))
	}

	// Helpful assertion: output should mention authentication or credentials
	stdout := strings.ToLower(string(out))
	if !strings.Contains(stdout, "failed to authenticate") && !strings.Contains(stdout, "credentials") && !strings.Contains(stdout, "no valid credential") {
		t.Fatalf("expected auth-related error message, got: %s", stdout)
	}
}
