package patcher

import "testing"

func TestFindHunkMatchAllowsSoftWhitespace(t *testing.T) {
	lines := []string{
		"Copyright (c)    2012 Foo",
		"Permission is hereby granted",
	}
	h := Hunk{
		ContextBefore: nil,
		Deletions:     []string{"Copyright (c) 2012 Foo"},
		ContextAfter:  []string{"Permission is hereby granted"},
	}
	idx, exact, _, _, err := FindHunkMatch(lines, h, -1)
	if err != nil {
		t.Fatalf("FindHunkMatch returned error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("FindHunkMatch index = %d, want 0", idx)
	}
	if exact {
		t.Fatalf("expected lenient match, got exact")
	}
}

func TestFindHunkMatchTrimsEdgeWhitespace(t *testing.T) {
	lines := []string{
		"  Header",
		"Body line",
		"Footer",
	}
	h := Hunk{
		ContextBefore: []string{"Header"},
		Deletions:     []string{"Body line"},
		ContextAfter:  []string{"Footer"},
	}
	idx, exact, _, _, err := FindHunkMatch(lines, h, 0)
	if err != nil {
		t.Fatalf("FindHunkMatch returned error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("FindHunkMatch index = %d, want 0", idx)
	}
	if exact {
		t.Fatalf("expected lenient match for leading/trailing whitespace")
	}
}
