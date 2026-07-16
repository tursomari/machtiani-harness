package utils

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RequireExitCode asserts the exit code matches the expected value.
func RequireExitCode(t *testing.T, result ShellResult, expected int) {
	t.Helper()
	if result.ExitCode != expected {
		t.Fatalf("expected exit code %d, got %d (stdout=%q, stderr=%q)", expected, result.ExitCode, result.Stdout, result.Stderr)
	}
}

// RequireStdoutContains asserts the stdout stream includes the provided substring.
func RequireStdoutContains(t *testing.T, result ShellResult, substr string) {
	t.Helper()
	if !strings.Contains(result.Stdout, substr) {
		t.Fatalf("expected stdout to include %q; got %q", substr, result.Stdout)
	}
}

// RequireStderrContains asserts the stderr stream includes the provided substring.
func RequireStderrContains(t *testing.T, result ShellResult, substr string) {
	t.Helper()
	if !strings.Contains(result.Stderr, substr) {
		t.Fatalf("expected stderr to include %q; got %q", substr, result.Stderr)
	}
}

// RequireDurationUnder bounds the execution duration to the provided threshold.
func RequireDurationUnder(t *testing.T, result ShellResult, max time.Duration) {
	t.Helper()
	if result.Duration > max {
		t.Fatalf("expected command to finish under %s; took %s", max, result.Duration)
	}
}

// LoadJSONFixture reads a JSON fixture relative to the repository root.
func LoadJSONFixture(t *testing.T, baseDir, name string, target any) {
	t.Helper()
	path := filepath.Join(baseDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
}

// RequireStdoutMatchesJSONFixture validates stdout JSON against a fixture payload.
func RequireStdoutMatchesJSONFixture(t *testing.T, stdout, fixturePath string) {
	t.Helper()

	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		t.Fatalf("stdout is empty; expected JSON payload matching %s", fixturePath)
	}

	var expected any
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatalf("parse fixture %s: %v", fixturePath, err)
	}

	var actual any
	if err := json.Unmarshal([]byte(stdout), &actual); err != nil {
		t.Fatalf("stdout does not contain JSON: %v\npayload: %s", err, stdout)
	}

	if !deepEqual(actual, expected) {
		t.Fatalf("stdout JSON does not match fixture %s\nexpected: %s\nactual: %s", fixturePath, string(data), stdout)
	}
}

func deepEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return false
		}
		if len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		if !ok {
			return false
		}
		return av == bv
	case string:
		bv, ok := b.(string)
		if !ok {
			return false
		}
		return av == bv
	case bool:
		bv, ok := b.(bool)
		if !ok {
			return false
		}
		return av == bv
	case nil:
		return b == nil
	default:
		return false
	}
}
