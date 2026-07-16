package llm

import (
	"strings"
	"testing"
)

func TestPromptMaterialPreservesFixedAndTrimsInPriorityOrder(t *testing.T) {
	material := PromptMaterial{
		Fixed: "REQUIRED README INSTRUCTIONS",
		Sections: []PromptSection{
			{Name: "metadata", Body: strings.Repeat("hash metadata\n", 20), TrimPriority: 1, OmitFirst: true},
			{Name: "diff detail", Prefix: "DIFF\n", Body: strings.Repeat("diff detail line\n", 40), TrimPriority: 2},
			{Name: "summaries", Body: strings.Repeat("summary hint\n", 20), TrimPriority: 3},
			{Name: "previous README", Body: strings.Repeat("previous readme line\n", 20), TrimPriority: 4},
		},
	}
	full, err := material.Render(0)
	if err != nil {
		t.Fatal(err)
	}
	limit := full.TokenCount - EstimateTokens(strings.Repeat("hash metadata\n", 20)) + 1
	fitted, err := material.Render(limit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fitted.Text, "REQUIRED README INSTRUCTIONS") {
		t.Fatal("fixed instructions were removed")
	}
	if strings.Contains(fitted.Text, "hash metadata") {
		t.Fatal("metadata was not removed first")
	}
	if !strings.Contains(fitted.Text, "previous readme line") {
		t.Fatal("lower-priority previous README was trimmed too early")
	}
	if err := RequireWithinTokenBudget("fitted", fitted.Text, limit); err != nil {
		t.Fatal(err)
	}
}

func TestPromptMaterialRejectsFixedContentOverBudget(t *testing.T) {
	_, err := (PromptMaterial{Fixed: strings.Repeat("required ", 50)}).Render(2)
	if err == nil || !strings.Contains(err.Error(), "fixed prompt content") {
		t.Fatalf("error = %v", err)
	}
}

func TestTruncateLinesKeepTail(t *testing.T) {
	input := "old one\nold two\nnew three\nnew four\n"
	got, truncated, err := TruncateLinesKeepTail(input, 4, "TRUNCATED")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || !strings.Contains(got, "new four") || strings.Contains(got, "old one") {
		t.Fatalf("unexpected truncation: %q", got)
	}
}
