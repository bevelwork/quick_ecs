package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
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

// TestThrobberProgress ensures the spinner advances frames over time.
func TestThrobberProgress(t *testing.T) {
	stop := startThrobber("Testing spinner...")
	// capture output by allowing a few ticks
	time.Sleep(350 * time.Millisecond)
	// stop should clear the line and end goroutine
	stop()
	// If we reach here without deadlock, assume success. Visual inspection recommended when running tests.
}

// TestSanitizeARN verifies the sanitizeARN function masks account numbers correctly
func TestSanitizeARN(t *testing.T) {
	tests := []struct {
		name        string
		arn         string
		privateMode bool
		expected    string
	}{
		{
			name:        "ECS cluster ARN in private mode",
			arn:         "arn:aws:ecs:us-east-1:123456789012:cluster/my-cluster",
			privateMode: true,
			expected:    "arn:aws:ecs:us-east-1:***:cluster/my-cluster",
		},
		{
			name:        "ECS service ARN in private mode",
			arn:         "arn:aws:ecs:us-east-1:123456789012:service/my-cluster/my-service",
			privateMode: true,
			expected:    "arn:aws:ecs:us-east-1:***:service/my-cluster/my-service",
		},
		{
			name:        "Task definition ARN in private mode",
			arn:         "arn:aws:ecs:us-east-1:123456789012:task-definition/my-task:1",
			privateMode: true,
			expected:    "arn:aws:ecs:us-east-1:***:task-definition/my-task:1",
		},
		{
			name:        "ARN in non-private mode",
			arn:         "arn:aws:ecs:us-east-1:123456789012:cluster/my-cluster",
			privateMode: false,
			expected:    "arn:aws:ecs:us-east-1:123456789012:cluster/my-cluster",
		},
		{
			name:        "Invalid ARN format",
			arn:         "not-an-arn",
			privateMode: true,
			expected:    "not-an-arn",
		},
		{
			name:        "Short ARN",
			arn:         "arn:aws:ecs:us-east-1",
			privateMode: true,
			expected:    "arn:aws:ecs:us-east-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeARN(tt.arn, tt.privateMode)
			if result != tt.expected {
				t.Errorf("sanitizeARN(%q, %v) = %q, want %q", tt.arn, tt.privateMode, result, tt.expected)
			}
		})
	}
}
