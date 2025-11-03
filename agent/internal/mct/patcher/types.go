package patcher

import (
	"context"
)

// Service defines the behavior required for applying instructions and generating patches.
type Service interface {
	ApplyAndGeneratePatch(ctx context.Context, params PatchParams) (*PatchResult, error)
	ValidateInstructions(ctx context.Context, repoRoot string, instructions Instructions) error
}

// PatchParams holds the inputs for producing a patch from a repository.
type PatchParams struct {
	RepoRoot      string
	SessionID     string
	Instructions  Instructions
	OutputDir     string
	Verbose       bool
	WorkspaceRoot string
	MirrorDir     string
	Sequence      int
}

// PatchResult contains metadata about a generated patch file.
type PatchResult struct {
	PatchPath          string
	FilesModified      []string
	Insertions         int
	Deletions          int
	Description        string
	AfterStateDir      string
	ManifestPath       string
	Sequence           int
	AppliedInWorkspace bool
	ReversePatchPath   string
}

// Instructions encodes the set of file edits that should be applied.
type Instructions struct {
	Edits    []Edit    `json:"edits"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Metadata is optional descriptive metadata supplied alongside Instructions.
type Metadata struct {
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	Email       string `json:"email,omitempty"`
}

// Mode describes how a particular edit should be applied to a file.
type Mode string

const (
	ModeReplace Mode = "replace"
	ModeRewrite Mode = "rewrite"
	ModeCreate  Mode = "create"
	ModeDelete  Mode = "delete"
)

// Edit represents a single file edit to apply when synthesizing a patch.
type Edit struct {
	Path       string `json:"path"`
	Mode       Mode   `json:"mode"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}
