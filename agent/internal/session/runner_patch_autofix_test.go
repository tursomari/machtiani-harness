package session

import (
	"fmt"
	"reflect"
	"testing"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func TestConvertRewriteMissingToCreate(t *testing.T) {
	meta := &mctpatcher.Metadata{Description: "test"}
	in := mctpatcher.Instructions{
		Metadata: meta,
		Edits: []mctpatcher.Edit{
			{Mode: mctpatcher.ModeRewrite, NewContent: "content"},
			{Mode: mctpatcher.ModeCreate, NewContent: "other"},
		},
	}
	err := &mctpatcher.ValidationError{Err: fmt.Errorf("invalid instructions: edit[0] rewrite requires existing file")}
	out, changed := convertRewriteMissingToCreate(in, err, false)
	if len(changed) != 1 || changed[0] != 0 {
		t.Fatalf("expected change for index 0, got %v", changed)
	}
	if out.Metadata != meta {
		t.Fatalf("expected metadata pointer preserved")
	}
	if got := out.Edits[0].Mode; got != mctpatcher.ModeCreate {
		t.Fatalf("expected mode create, got %s", got)
	}
	if got := out.Edits[1].Mode; got != mctpatcher.ModeCreate {
		t.Fatalf("expected second edit unchanged, got %s", got)
	}
	// Ensure original instructions not mutated.
	if in.Edits[0].Mode != mctpatcher.ModeRewrite {
		t.Fatalf("original instructions mutated")
	}
}

func TestConvertRewriteMissingToCreateIgnoresInvalidCases(t *testing.T) {
	cases := []struct {
		name     string
		instr    mctpatcher.Instructions
		err      *mctpatcher.ValidationError
		expected []int
	}{
		{
			name:     "no matches",
			instr:    mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Mode: mctpatcher.ModeRewrite, NewContent: "x"}}},
			err:      &mctpatcher.ValidationError{Err: fmt.Errorf("invalid instructions: edit[0] create requires missing file")},
			expected: nil,
		},
		{
			name:     "missing new content",
			instr:    mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Mode: mctpatcher.ModeRewrite}}},
			err:      &mctpatcher.ValidationError{Err: fmt.Errorf("invalid instructions: edit[0] rewrite requires existing file")},
			expected: nil,
		},
	}

	for _, tc := range cases {
		out, changed := convertRewriteMissingToCreate(tc.instr, tc.err, false)
		if len(changed) != len(tc.expected) {
			t.Fatalf("%s: expected %d changes, got %d", tc.name, len(tc.expected), len(changed))
		}
		if !reflect.DeepEqual(out.Edits, tc.instr.Edits) {
			t.Fatalf("%s: edits mutated unexpectedly", tc.name)
		}
	}
}
