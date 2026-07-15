package llm

import "testing"

func TestResolveInputBudgetPrecedenceAndMath(t *testing.T) {
	model := ResolvedModel{ContextLength: 128000, ContextSource: SourceFile}
	budget, err := ResolveInputBudget(model, 64000)
	if err != nil {
		t.Fatal(err)
	}
	if budget.ContextLength != 64000 || budget.Source != ContextSourceSessionFlag {
		t.Fatalf("budget = %#v", budget)
	}
	if budget.CompletionReserve != 8000 || budget.SafetyMargin != 3200 || budget.MaxInputTokens != 52800 {
		t.Fatalf("unexpected arithmetic: %#v", budget)
	}
}

func TestResolveInputBudgetCompiledDefault(t *testing.T) {
	budget, err := ResolveInputBudget(ResolvedModel{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if budget.ContextLength != DefaultContextLength || budget.Source != ContextSourceCompiledDefault {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestResolveInputBudgetRuntimeLearned(t *testing.T) {
	model := ResolvedModel{ContextLength: 64000, ContextSource: SourceFile, ContextLearned: true}
	budget, err := ResolveInputBudget(model, 0)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Source != ContextSourceRuntimeLearned {
		t.Fatalf("source = %q", budget.Source)
	}
}

func TestContextLengthForInputCap(t *testing.T) {
	length := ContextLengthForInputCap(50000)
	budget, err := BudgetForContextLength(length, ContextSourceRuntimeLearned)
	if err != nil || budget.MaxInputTokens < 50000 {
		t.Fatalf("length=%d budget=%#v err=%v", length, budget, err)
	}
	if previous, _ := BudgetForContextLength(length-1, ContextSourceRuntimeLearned); previous.MaxInputTokens >= 50000 {
		t.Fatalf("length %d was not minimal", length)
	}
}
