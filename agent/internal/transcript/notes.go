package transcript

import (
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/templates"
)

const (
	patchSuccessNoteTemplateKey = "mct.patch_success_note"
	fullDiffNoteTemplateKey     = "mct.full_diff_note"
)

var (
	notesOnce           sync.Once
	defaultPatchNote    string
	defaultFullDiffNote string
)

func loadDefaultNotes() {
	if embedded, err := templates.GetEmbeddedTemplate(patchSuccessNoteTemplateKey); err == nil {
		defaultPatchNote = strings.TrimSpace(embedded)
	}
	if embedded, err := templates.GetEmbeddedTemplate(fullDiffNoteTemplateKey); err == nil {
		defaultFullDiffNote = strings.TrimSpace(embedded)
	}
}

func PatchSuccessNoteText(cfg *llm.MCTPromptsConfig) string {
	if cfg != nil {
		if trimmed := strings.TrimSpace(cfg.PatchSuccessNote); trimmed != "" {
			return trimmed
		}
	}
	notesOnce.Do(loadDefaultNotes)
	return defaultPatchNote
}

func FullDiffNoteText(cfg *llm.MCTPromptsConfig) string {
	if cfg != nil {
		if trimmed := strings.TrimSpace(cfg.FullDiffNote); trimmed != "" {
			return trimmed
		}
	}
	notesOnce.Do(loadDefaultNotes)
	return defaultFullDiffNote
}
