package sessionfiles

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		want DisposableKind
	}{
		{"artifacts/llm/inputs.jsonl", DisposableLLMInputLog},
		{"shell-agent/0/state.json", DisposableShellAgentState},
		{"shell-agent/12/state.json", DisposableShellAgentState},
		{"trajectory/agent.jsonl", DisposableNone},
		{"shell-agent/12/trajectory.json", DisposableNone},
		{"shell-agent/not-a-turn/state.json", DisposableNone},
		{"state.json", DisposableNone},
		{"artifacts/llm/other.jsonl", DisposableNone},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := Classify(test.path); got != test.want {
				t.Fatalf("Classify(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
