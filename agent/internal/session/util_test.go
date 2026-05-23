package session

import (
	"strings"
	"testing"
)

func TestApplySingleAskRoutingPolicy_SingleAskForcesShell(t *testing.T) {
	useShell, forced := applySingleAskRoutingPolicy(false, false)
	if !useShell {
		t.Fatalf("expected single ask to route to shell")
	}
	if !forced {
		t.Fatalf("expected single ask route to be marked forced")
	}
}

func TestCollapseSplitAskLines(t *testing.T) {
	got := collapseSplitAskLines("Explain config loading.", "Run `git diff --stat` and summarize recent changes.")
	if strings.Contains(got, "No-shell:") || strings.Contains(got, "Shell:") {
		t.Fatalf("expected collapsed content, got %q", got)
	}
	if !strings.Contains(got, "Explain config loading.") || !strings.Contains(got, "git diff --stat") {
		t.Fatalf("unexpected collapsed content: %q", got)
	}
}

func TestApplySingleAskRoutingPolicy_SplitAskUnchanged(t *testing.T) {
	useShell, forced := applySingleAskRoutingPolicy(true, false)
	if useShell {
		t.Fatalf("expected split ask routing decision to remain unchanged")
	}
	if forced {
		t.Fatalf("did not expect forced routing for split ask")
	}
}

func TestApplySingleAskRoutingPolicy_ExistingShellSelectionPreserved(t *testing.T) {
	useShell, forced := applySingleAskRoutingPolicy(false, true)
	if !useShell {
		t.Fatalf("expected existing shell selection to remain enabled")
	}
	if forced {
		t.Fatalf("did not expect forced routing when shell is already selected")
	}
}
