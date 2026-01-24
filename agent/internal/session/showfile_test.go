package session

import (
	"fmt"
	"testing"
)

func TestMergeShowFilePathsPreservesOrderAndDedupe(t *testing.T) {
	explicit := []string{"./foo/bar.go", "baz/../baz.go", ""}
	discovered := []string{"foo/bar.go", "new.go", "./new.go"}

	got := mergeShowFilePaths(explicit, discovered)
	want := []string{"foo/bar.go", "baz.go", "new.go"}
	if len(got) != len(want) {
		t.Fatalf("unexpected merge length: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected path at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestMergeShowFilePathsIncludesAllDiscovered(t *testing.T) {
	explicit := []string{"a.go"}
	var discovered []string
	for i := 0; i < 12; i++ {
		discovered = append(discovered, fmt.Sprintf("extra-%d.go", i))
	}

	got := mergeShowFilePaths(explicit, discovered)
	if len(got) != 13 {
		t.Fatalf("unexpected merge length: got %d want %d", len(got), 13)
	}
	if got[0] != "a.go" {
		t.Fatalf("unexpected first path: %q", got[0])
	}
	if got[len(got)-1] != "extra-11.go" {
		t.Fatalf("unexpected last path: %q", got[len(got)-1])
	}
}
