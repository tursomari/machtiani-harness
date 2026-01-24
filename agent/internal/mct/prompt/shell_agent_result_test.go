package prompt

import "testing"

func TestExtractShellAgentResultBlock(t *testing.T) {
	output := "header\n" +
		shellAgentResultBlockBegin + "\n" +
		"Exit Status: ok\n" +
		"Result: line one\nline two\n" +
		shellAgentResultBlockEnd + "\n" +
		"footer"

	block, ok := extractShellAgentResultBlock(output)
	if !ok {
		t.Fatalf("expected to extract result block")
	}
	expected := "Exit Status: ok\nResult: line one\nline two"
	if block != expected {
		t.Fatalf("expected block %q, got %q", expected, block)
	}
}

func TestExtractShellAgentResultBlockMissing(t *testing.T) {
	block, ok := extractShellAgentResultBlock("no markers here")
	if ok {
		t.Fatalf("expected missing block, got %q", block)
	}
}

func TestExtractShellAgentResultBlockEmpty(t *testing.T) {
	output := shellAgentResultBlockBegin + "\n" + shellAgentResultBlockEnd
	block, ok := extractShellAgentResultBlock(output)
	if ok {
		t.Fatalf("expected empty block to be ignored, got %q", block)
	}
}
