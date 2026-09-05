package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestVerifyCommandInvokesEveryConfiguredModelRoleThroughMachtiani(t *testing.T) {
	root := t.TempDir()
	capture := filepath.Join(root, "requests.jsonl")
	processCapture := filepath.Join(root, "processes.txt")
	command := filepath.Join(root, "model-host")
	script := `#!/bin/sh
read request
printf '%s\n' "$request" >> "$MACHTIANI_VERIFY_CAPTURE"
printf '%s\n' "$$" >> "$MACHTIANI_VERIFY_PROCESS_CAPTURE"
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"text-delta","index":0,"text":"READY"}}'
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"usage","inputTokens":11,"outputTokens":1,"totalTokens":12,"cacheReadTokens":7,"reasoningTokens":3}}'
printf '%s\n' '{"v":1,"id":"generation","result":{"completed":true}}'
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `default_model = "planner-model"
shell_agent_model = "shell-model"
answer_model = "answer-model"
file_discovery_model = "discovery-model"

[providers.shared]
transport = "model-host"
profile = "/private/provider-profile.json"
command = ` + jsonQuote(command) + `

[models.planner-model]
provider = "shared"
model = "provider-planner"
context_length = 131072

[models.shell-model]
provider = "shared"
model = "provider-shell"
context_length = 131072

[models.answer-model]
provider = "shared"
model = "provider-answer"
context_length = 131072

[models.discovery-model]
provider = "shared"
model = "provider-discovery"
context_length = 131072
`
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	t.Setenv("MACHTIANI_VERIFY_CAPTURE", capture)
	t.Setenv("MACHTIANI_VERIFY_PROCESS_CAPTURE", processCapture)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)
	loaded, _, err := llm.LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ShellAgentModel != "shell-model" || loaded.AnswerModel != "answer-model" || loaded.FileDiscoveryModel != "discovery-model" {
		t.Fatalf("configured aliases were not loaded: %+v", configuredVerificationRoles(loaded))
	}

	var code int
	stdout := captureStdout(t, func() {
		code = handleVerifyCommand([]string{"--json", "--timeout", "2s"})
	})
	if code != 0 {
		t.Fatalf("verify exit=%d output=%s", code, stdout)
	}
	var report struct {
		Status string `json:"status"`
		Roles  []struct {
			Role            string `json:"role"`
			Alias           string `json:"alias"`
			Model           string `json:"model"`
			CachedTokens    int    `json:"cachedTokens"`
			ReasoningTokens int    `json:"reasoningTokens"`
		} `json:"roles"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || len(report.Roles) != 4 {
		t.Fatalf("report=%+v", report)
	}
	wantRoles := []string{"planner", "shell-agent", "answer", "file-discovery"}
	wantAliases := []string{"planner-model", "shell-model", "answer-model", "discovery-model"}
	wantModels := []string{"provider-planner", "provider-shell", "provider-answer", "provider-discovery"}
	for index := range wantRoles {
		role := report.Roles[index]
		if role.Role != wantRoles[index] || role.Alias != wantAliases[index] || role.Model != wantModels[index] || role.CachedTokens != 7 || role.ReasoningTokens != 3 {
			t.Fatalf("role[%d]=%+v", index, role)
		}
	}

	payload, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitNonemptyLines(string(payload))
	if len(lines) != 4 {
		t.Fatalf("captured %d requests", len(lines))
	}
	sessions := map[string]bool{}
	for index, line := range lines {
		var request struct {
			Params struct {
				Caller    string `json:"caller"`
				Role      string `json:"role"`
				SessionID string `json:"sessionId"`
				Model     string `json:"model"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Caller != "machtiani" || request.Params.Role != wantRoles[index] || request.Params.Model != wantModels[index] {
			t.Fatalf("request[%d]=%+v", index, request.Params)
		}
		if request.Params.SessionID == "" || sessions[request.Params.SessionID] {
			t.Fatalf("session identity is empty or reused: %q", request.Params.SessionID)
		}
		sessions[request.Params.SessionID] = true
	}
	processes, err := os.ReadFile(processCapture)
	if err != nil {
		t.Fatal(err)
	}
	processIDs := splitNonemptyLines(string(processes))
	if len(processIDs) != 4 {
		t.Fatalf("started %d model-host processes, want one fresh process per role", len(processIDs))
	}
	uniqueProcesses := map[string]bool{}
	for _, processID := range processIDs {
		if uniqueProcesses[processID] {
			t.Fatalf("model-host process was reused across roles: %v", processIDs)
		}
		uniqueProcesses[processID] = true
	}
}

func TestVerifyCommandRejectsNonModelHostRoleBeforeInference(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	config := `default_model = "direct"
shell_agent_model = "direct"
answer_model = "direct"
file_discovery_model = "direct"

[providers.direct]
base_url = "https://example.invalid/v1"
api_key = "not-a-real-key"

[models.direct]
provider = "direct"
model = "direct-model"
context_length = 131072
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	var code int
	stderr := captureStderr(t, func() {
		code = handleVerifyCommand([]string{"--timeout", time.Second.String()})
	})
	if code != 1 || !containsAll(stderr, "planner", "does not use the shared model host") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

func TestActionableModelHostErrorGuidance(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "expired", err: &llm.ModelHostCallError{Code: "AUTH_EXPIRED", Message: "Sign in expired."}, want: []string{"Sign in expired.", "machtiani auth login --model shared"}},
		{name: "rate", err: &llm.ModelHostCallError{Code: "RATE_LIMITED", Message: "Slow down.", RetryAfterMS: 2500}, want: []string{"Slow down.", "Retry after 3s"}},
		{name: "quota", err: &llm.ModelHostCallError{Code: "QUOTA_EXHAUSTED", Message: "No usage."}, want: []string{"No usage.", "subscription account's usage limits"}},
		{name: "model", err: &llm.ModelHostCallError{Code: "MODEL_UNAVAILABLE", Message: "Gone."}, want: []string{"Gone.", "MODEL_UNAVAILABLE"}},
		{name: "ordinary", err: errors.New("plain failure"), want: []string{"plain failure"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := actionableModelHostError("shared", test.err)
			if !containsAll(got, test.want...) {
				t.Fatalf("guidance=%q want=%v", got, test.want)
			}
		})
	}
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func splitNonemptyLines(value string) []string {
	var result []string
	start := 0
	for index := 0; index <= len(value); index++ {
		if index != len(value) && value[index] != '\n' {
			continue
		}
		if index > start {
			result = append(result, value[start:index])
		}
		start = index + 1
	}
	return result
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
