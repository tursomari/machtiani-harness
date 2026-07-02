package ui

// PromptStream defines the interface for streaming prompt output.
type PromptStream interface {
	OnChunk(chunk string)
	Complete(finalText string)
	Abort(message string)
}

var _ PromptStream = (*FormatterPromptStream)(nil)
