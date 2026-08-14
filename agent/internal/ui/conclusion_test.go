package ui

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/presentation"
)

func conclusionTestTheme(glyphs presentation.GlyphMode) Theme {
	return DefaultTheme(presentation.NewForTestWithGlyphs(presentation.ProfileTerminal, glyphs, false, false))
}

func TestRenderCompletedSessionConclusion(t *testing.T) {
	event := SessionConclusionEvent{
		Outcome:         SessionConclusionCompleted,
		RenderedAnswer:  "  Completed the requested work.",
		FinalAnswerPath: "/home/test/.machtiani/project/sessions/agent-test/chat/agent-final-answer.md",
		SessionID:       "agent-test",
		Verbose:         true,
		TurnsCompleted:  3,
		Goal:            "Improve the conclusion",
	}
	t.Setenv("HOME", "/home/test")
	output := stripANSI(RenderSessionConclusion(event, conclusionTestTheme(presentation.GlyphUnicode), 120))
	for _, want := range []string{
		"  ━━━━━━━━━",
		"  Completed the requested work.",
		"  Answer saved to:\n    ~/.machtiani/project/sessions/agent-test/chat/agent-final-answer.md",
		"  Session ID: agent-test",
		"  Turns completed: 3",
		`  Goal so far: "Improve the conclusion"`,
		"  Resume this session:",
		`    $ mct-agent run -p "<your follow-up prompt>" --resume agent-test`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"FINAL RESPONSE", "This answer is also available at:", "<next instruction>"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("unexpected %q in:\n%s", forbidden, output)
		}
	}
}

func TestRenderCompletedConclusionClosesAnswerBeforeProvenance(t *testing.T) {
	event := SessionConclusionEvent{
		Outcome:         SessionConclusionCompleted,
		RenderedAnswer:  "  Completed the requested work.",
		FinalAnswerPath: "/home/test/.machtiani/project/sessions/agent-test/chat/agent-final-answer.md",
		SessionID:       "agent-test",
	}
	t.Setenv("HOME", "/home/test")
	output := stripANSI(RenderSessionConclusion(event, conclusionTestTheme(presentation.GlyphUnicode), 120))
	outerRule := "  " + strings.Repeat("━", conclusionOuterRuleWidth(120))
	want := "  Completed the requested work.\n\n" + outerRule + "\n\n" +
		"  Answer saved to:\n"
	if !strings.Contains(output, want) {
		t.Fatalf("completed-answer boundary mismatch\nwant substring: %q\noutput:\n%s", want, output)
	}
	if got := strings.Count(output, outerRule); got != 2 {
		t.Fatalf("outer rule count = %d, want 2\n%s", got, output)
	}
	if strings.LastIndex(output, outerRule) > strings.Index(output, "  Answer saved to:") {
		t.Fatalf("completed-answer rule enclosed provenance and actions:\n%s", output)
	}
}

func TestRenderInterruptedSessionConclusion(t *testing.T) {
	output := stripANSI(RenderSessionConclusion(SessionConclusionEvent{
		Outcome:   SessionConclusionShellInterrupted,
		SessionID: "agent-interrupted",
	}, conclusionTestTheme(presentation.GlyphUnicode), 100))
	for _, want := range []string{
		"  SHELL-AGENT INTERRUPTED",
		"  Shell-agent work is resumable.",
		"  Resume the interrupted shell-agent work:",
		"    $ mct-agent run --resume agent-interrupted",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"Answer saved", " -p ", "Final answer"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("unexpected %q in:\n%s", forbidden, output)
		}
	}
}

func TestRenderUserInputSessionConclusion(t *testing.T) {
	output := stripANSI(RenderSessionConclusion(SessionConclusionEvent{
		Outcome:     SessionConclusionUserInputNeeded,
		SessionID:   "agent-question",
		Explanation: "The safer fix preserves existing behavior.",
		Question:    "Do you want the safer fix?",
	}, conclusionTestTheme(presentation.GlyphUnicode), 100))
	want := "  USER INPUT NEEDED\n\n" +
		"  Why this needs your input:\n" +
		"    The safer fix preserves existing behavior.\n\n" +
		"  Your decision:\n" +
		"    ? Do you want the safer fix?\n\n" +
		"  Continue with your answer:"
	if !strings.Contains(output, want) {
		t.Fatalf("user-input hierarchy mismatch\nwant: %q\ngot:\n%s", want, output)
	}
	if strings.Contains(output, "Session ID:") {
		t.Fatalf("duplicated session ID metadata:\n%s", output)
	}
	if !strings.Contains(output, `    $ mct-agent run -p "<your answer>" --resume agent-question`) {
		t.Fatalf("missing answer command:\n%s", output)
	}
}

func TestRenderUserInputConclusionOmitsEmptyExplanation(t *testing.T) {
	output := RenderSessionConclusion(SessionConclusionEvent{
		Outcome:   SessionConclusionUserInputNeeded,
		SessionID: "agent-question",
		Question:  "Which option?",
	}, conclusionTestTheme(presentation.GlyphUnicode), 100)
	if strings.Contains(output, "Why this needs your input:") {
		t.Fatalf("empty explanation section was rendered:\n%s", output)
	}
}

func TestConclusionCommandUsesCanonicalMultilineBashWhenNarrow(t *testing.T) {
	output := stripANSI(RenderSessionConclusion(SessionConclusionEvent{
		Outcome:   SessionConclusionCompleted,
		SessionID: "agent-20260718T051605-0813",
	}, conclusionTestTheme(presentation.GlyphUnicode), 72))
	want := "    mct-agent run \\\n" +
		"      -p \"<your follow-up prompt>\" \\\n" +
		"      --resume agent-20260718T051605-0813"
	if !strings.Contains(output, want) {
		t.Fatalf("multiline command mismatch\nwant: %q\ngot:\n%s", want, output)
	}
	if strings.Contains(output, "    $ mct-agent run") {
		t.Fatalf("multiline command retained shell prompt:\n%s", output)
	}
	lines := strings.Split(output, "\n")
	var ruleWidths []int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			ruleWidths = append(ruleWidths, len([]rune(trimmed)))
		}
	}
	if len(ruleWidths) != 2 || ruleWidths[0] != ruleWidths[1] {
		t.Fatalf("command rules not paired: %v\n%s", ruleWidths, output)
	}
}

func TestConclusionCommandRuleEndsTwoColumnsAfterSingleLineCommand(t *testing.T) {
	output := stripANSI(RenderSessionConclusion(SessionConclusionEvent{
		Outcome:   SessionConclusionCompleted,
		SessionID: "agent-test",
	}, conclusionTestTheme(presentation.GlyphUnicode), 120))
	var command, rule string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "$ mct-agent run") {
			command = trimmed
		}
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			rule = trimmed
		}
	}
	if command == "" || rule == "" {
		t.Fatalf("missing command enclosure:\n%s", output)
	}
	if len([]rune(rule)) != len([]rune(command))+2 {
		t.Fatalf("rule width = %d, command width = %d; want two columns right padding", len([]rune(rule)), len([]rune(command)))
	}
}

func TestASCIIConclusionUsesOnlyASCIIOwnedGlyphs(t *testing.T) {
	output := stripANSI(RenderSessionConclusion(SessionConclusionEvent{
		Outcome:        SessionConclusionCompleted,
		RenderedAnswer: "  User content keeps café.",
		SessionID:      "agent-ascii",
	}, conclusionTestTheme(presentation.GlyphASCII), 100))
	if !strings.Contains(output, "  ====") || !strings.Contains(output, "    ----") {
		t.Fatalf("missing ASCII rule hierarchy:\n%s", output)
	}
	for _, forbidden := range []string{"━", "─"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("ASCII output contains %q:\n%s", forbidden, output)
		}
	}
	if !strings.Contains(output, "café") {
		t.Fatalf("ASCII mode rewrote content:\n%s", output)
	}
}

func TestConclusionOuterRuleShrinksWithoutWrapping(t *testing.T) {
	output := RenderSessionConclusion(SessionConclusionEvent{
		Outcome:   SessionConclusionShellInterrupted,
		SessionID: "short",
	}, conclusionTestTheme(presentation.GlyphUnicode), 40)
	for _, line := range strings.Split(stripANSI(output), "\n") {
		if strings.Contains(line, "━") && len([]rune(line)) >= 40 {
			t.Fatalf("outer rule reached terminal wrap column: width=%d line=%q", len([]rune(line)), line)
		}
	}
}
