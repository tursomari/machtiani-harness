package tools

// Tool describes a function-call tool exposed to the LLM.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]interface{}
	Yields      bool
}

var FSRead = Tool{
	Name:        "FSRead",
	Description: "Reads a file at the given path with optional line offset and limit.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Line offset (0-indexed, default 0).",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Max lines to read (default 200).",
			},
		},
		"required": []string{"path"},
	},
	Yields: false,
}

var FSWrite = Tool{
	Name:        "FSWrite",
	Description: "Writes content to a file at the given path. Requires a prior read of the same path.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Full content to write.",
			},
			"overwrite": map[string]interface{}{
				"type":        "boolean",
				"description": "Allow overwriting an existing file.",
			},
		},
		"required": []string{"path", "content"},
	},
	Yields: false,
}

var FSPatch = Tool{
	Name:        "FSPatch",
	Description: "Applies a single find-and-replace patch to a file. Requires a prior read of the same path.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "Exact string to find (must appear exactly once unless replace_all is true).",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "Replacement string.",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "Replace all occurrences instead of just the first.",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	},
	Yields: false,
}

var FSMultiPatch = Tool{
	Name:        "FSMultiPatch",
	Description: "Applies multiple find-and-replace patches atomically to a file. Requires a prior read.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
			"patches": map[string]interface{}{
				"type":        "array",
				"description": "List of patches to apply sequentially.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old_string": map[string]interface{}{
							"type":        "string",
							"description": "String to find.",
						},
						"new_string": map[string]interface{}{
							"type":        "string",
							"description": "Replacement string.",
						},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"path", "patches"},
	},
	Yields: false,
}

var FSRemove = Tool{
	Name:        "FSRemove",
	Description: "Deletes a file after saving its contents on an undo stack. Requires a prior read.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file.",
			},
		},
		"required": []string{"path"},
	},
	Yields: false,
}

var FSUndo = Tool{
	Name:        "FSUndo",
	Description: "Restores a file that was previously removed or modified to its last saved state.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to restore.",
			},
		},
		"required": []string{"path"},
	},
	Yields: false,
}

// AllTools returns every tool for use in catalog assembly or system prompts.
func AllTools() []Tool {
	return []Tool{FSRead, FSWrite, FSPatch, FSMultiPatch, FSRemove, FSUndo}
}
