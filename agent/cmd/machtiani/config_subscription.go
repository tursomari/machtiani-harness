package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/presentation"
)

// Authentication belongs to the official runtime. This file contains only a
// reference to its private directory, never a copy of its credentials.
type chatGPTProfile struct {
	Version        int    `json:"version"`
	Driver         string `json:"driver"`
	Provider       string `json:"provider"`
	AuthMethod     string `json:"authMethod"`
	Model          string `json:"model"`
	RuntimeProfile string `json:"runtimeProfile"`
}

type chatGPTModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ReasoningEfforts []string `json:"reasoningEfforts"`
}

type chatGPTConfigSetup struct {
	path, command, createdDir string
	profile                   chatGPTProfile
	saved                     bool
}

func newChatGPTConfigSetup() (*chatGPTConfigSetup, error) {
	command := strings.TrimSpace(os.Getenv("MACHTIANI_MODEL_HOST_BIN"))
	if command == "" {
		command = "machtiani-model-host"
	}
	command, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("ChatGPT setup requires machtiani-model-host from the Machtiani Installer runtime on PATH (or MACHTIANI_MODEL_HOST_BIN)")
	}
	command, err = filepath.Abs(command)
	if err != nil {
		return nil, err
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(configRoot, "machtiani", "harness-profiles")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, fmt.Errorf("create ChatGPT profile directory: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "chatgpt-")
	if err != nil {
		return nil, err
	}
	setup := &chatGPTConfigSetup{
		path: filepath.Join(dir, "model-profile.json"), command: command, createdDir: dir,
		profile: chatGPTProfile{Version: 1, Driver: "openai-codex-app-server", Provider: "openai-codex",
			AuthMethod: "subscription", Model: "pending-selection", RuntimeProfile: filepath.Join(dir, "codex")},
	}
	// Use an isolated Codex profile, so an existing API-key login cannot silently
	// turn the subscription setup into an API-billed configuration.
	if err := setup.setInitialModel("pending-selection"); err != nil {
		setup.close()
		return nil, err
	}
	return setup, nil
}

func existingChatGPTConfigSetup(entry any) (*chatGPTConfigSetup, error) {
	provider, ok := entry.(map[string]any)
	if !ok || provider["transport"] != "model-host" {
		return nil, nil
	}
	path, _ := provider["profile"].(string)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configured model profile: %w", err)
	}
	var profile chatGPTProfile
	if json.Unmarshal(data, &profile) != nil {
		return nil, fmt.Errorf("configured model profile is not valid JSON")
	}
	if profile.Provider != "openai-codex" {
		return nil, nil
	}
	if profile.Version != 1 || profile.Driver != "openai-codex-app-server" || profile.AuthMethod != "subscription" || profile.RuntimeProfile == "" {
		return nil, fmt.Errorf("configured ChatGPT subscription profile is invalid")
	}
	command, _ := provider["command"].(string)
	return &chatGPTConfigSetup{path: path, command: command, profile: profile}, nil
}

func (setup *chatGPTConfigSetup) close() {
	if setup.createdDir != "" && !setup.saved {
		// Only this attempt's newly allocated directory is owned by this cleanup.
		// Existing profiles and account sessions are never removed on cancellation.
		_ = os.RemoveAll(setup.createdDir)
	}
}

func (setup *chatGPTConfigSetup) setInitialModel(model string) error {
	if setup.createdDir == "" {
		return nil // Other aliases or the concierge may share this profile.
	}
	setup.profile.Model = model
	data, err := json.MarshalIndent(setup.profile, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(setup.path, append(data, '\n'), 0600)
}

func (setup *chatGPTConfigSetup) authenticate(noInteractive bool, theme presentation.Theme) error {
	result, fault, err := callModelHostControl(setup.command, setup.path, "auth/status")
	if err != nil {
		return fmt.Errorf("check ChatGPT sign-in: %w", err)
	}
	if fault != nil && fault.Code != "AUTH_REQUIRED" && fault.Code != "AUTH_EXPIRED" {
		return fmt.Errorf("ChatGPT sign-in check failed (%s)", fault.Code)
	}
	if fault == nil {
		var status struct {
			Authenticated *bool `json:"authenticated"`
		}
		if json.Unmarshal(result, &status) != nil || status.Authenticated == nil {
			return fmt.Errorf("model host returned an invalid ChatGPT sign-in status")
		}
		if *status.Authenticated {
			return nil
		}
	}
	if noInteractive {
		return fmt.Errorf("ChatGPT sign-in is required; rerun config add without --no-interactive")
	}
	mode, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "ChatGPT sign-in",
		"Sign in with the ChatGPT account that holds your subscription.", []initMenuOption{
			{label: "Browser sign-in", value: "browser"},
			{label: "Device code (SSH or headless)", value: "device_code"},
		}, theme)
	if err != nil {
		return err
	}
	if err := callInteractiveModelHostLogin(setup.command, setup.path, mode); err != nil {
		return err
	}
	return setup.authenticate(true, theme)
}

func (setup *chatGPTConfigSetup) selectModel(requested string, noInteractive bool, theme presentation.Theme) (chatGPTModel, error) {
	result, fault, err := callModelHostControl(setup.command, setup.path, "models/list")
	if err != nil {
		return chatGPTModel{}, fmt.Errorf("load ChatGPT models: %w", err)
	}
	if fault != nil {
		return chatGPTModel{}, fmt.Errorf("could not load ChatGPT models (%s); check machtiani auth status or sign in again", fault.Code)
	}
	var response struct {
		Provider string         `json:"provider"`
		Models   []chatGPTModel `json:"models"`
	}
	if json.Unmarshal(result, &response) != nil || response.Provider != "openai-codex" {
		return chatGPTModel{}, fmt.Errorf("model host returned an invalid ChatGPT model catalogue")
	}
	models := make([]chatGPTModel, 0, len(response.Models))
	seen := make(map[string]bool)
	for _, model := range response.Models {
		if model.ID == "" || strings.TrimSpace(model.ID) != model.ID || strings.IndexFunc(model.ID, unicode.IsControl) >= 0 || seen[model.ID] {
			continue
		}
		// Preserve provider names, but never render terminal control characters.
		model.Name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, model.Name)
		for _, effort := range model.ReasoningEfforts {
			if strings.IndexFunc(effort, unicode.IsControl) >= 0 {
				return chatGPTModel{}, fmt.Errorf("model host returned an invalid reasoning effort")
			}
		}
		seen[model.ID] = true
		models = append(models, model)
	}
	if len(models) == 0 {
		return chatGPTModel{}, fmt.Errorf("no ChatGPT models are available for this account")
	}
	selected := strings.TrimSpace(requested)
	if selected == "" {
		if noInteractive {
			return chatGPTModel{}, fmt.Errorf("--model is required with --no-interactive; use the wizard to browse ChatGPT models")
		}
		selected, err = promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "ChatGPT models",
			"Models available through your signed-in account. Choose a model; its alias comes next.", chatGPTModelOptions(models), theme)
		if err != nil {
			return chatGPTModel{}, err
		}
	}
	for _, model := range models {
		if model.ID == selected {
			return model, nil
		}
	}
	return chatGPTModel{}, fmt.Errorf("model %q is not in the available ChatGPT catalogue", selected)
}

func chatGPTModelOptions(models []chatGPTModel) []initMenuOption {
	options := make([]initMenuOption, 0, len(models))
	for _, model := range models {
		label := model.Name
		if label == "" {
			label = model.ID
		} else if label != model.ID {
			label += " (" + model.ID + ")"
		}
		options = append(options, initMenuOption{label: label, value: model.ID})
	}
	return options
}

func promptChatGPTReasoning(efforts []string, theme presentation.Theme) (string, error) {
	options := []initMenuOption{{label: "Provider default", value: ""}}
	for _, effort := range efforts {
		if effort != "" {
			options = append(options, initMenuOption{label: effort, value: effort})
		}
	}
	return promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Reasoning",
		"Choose an effort offered by this model or keep the provider default.", options, theme)
}
