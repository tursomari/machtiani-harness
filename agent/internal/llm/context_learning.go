package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

var learnedContexts sync.Map

func learnedContextKey(alias, provider, model string) string {
	return alias + "\x00" + provider + "\x00" + model
}

func rememberLearnedContext(model ResolvedModel, length int) {
	learnedContexts.Store(learnedContextKey(model.Alias, model.ProviderName, model.Model), length)
}

func applyLearnedContext(model *ResolvedModel) {
	if model == nil {
		return
	}
	if value, ok := learnedContexts.Load(learnedContextKey(model.Alias, model.ProviderName, model.Model)); ok {
		if length, valid := value.(int); valid && length >= MinimumContextLength && (model.ContextLength == 0 || length < model.ContextLength) {
			model.ContextLength = length
			model.ContextLearned = true
		}
	}
}

// PersistLearnedContext writes a successful lower context using compare-before-write
// semantics. A false persisted result with nil error means the model is direct or
// its configuration changed concurrently.
func PersistLearnedContext(model ResolvedModel, previous, learned int) (bool, error) {
	rememberLearnedContext(model, learned)
	if model.Alias == "" || learned < MinimumContextLength {
		return false, nil
	}
	path, err := ConfigPath()
	if err != nil {
		return false, err
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return false, err
	}
	models, ok := raw["models"].(map[string]any)
	if !ok {
		return false, nil
	}
	entry, ok := models[model.Alias].(map[string]any)
	if !ok || fmt.Sprint(entry["provider"]) != model.ProviderName || fmt.Sprint(entry["model"]) != model.Model {
		return false, nil
	}
	if current, exists := toInt(entry["context_length"]); exists && current != previous {
		return false, nil
	}
	entry["context_length"] = int64(learned)
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".context-config-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return false, err
	}
	if err := toml.NewEncoder(tmp).Encode(raw); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}
