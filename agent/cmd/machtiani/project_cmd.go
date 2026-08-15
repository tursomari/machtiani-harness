package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func init() {
	cliCommands = append(cliCommands, cliCommand{name: "project", description: "Inspect project identity and storage", handler: handleProjectCommand})
}

func handleProjectCommand(args []string) int {
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(os.Stderr, "Usage: machtiani project show [--json]")
		return 2
	}
	fs := pflag.NewFlagSet("machtiani project show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print project details as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("project show takes no positional arguments")
	}
	ctx, err := projectstore.Discover("")
	if err != nil {
		return configError(err)
	}
	config := ""
	if ctx.Status == projectstore.StatusInitialized {
		if ctx.ConfigScope == projectstore.ScopeProject {
			config = ctx.ProjectConfigPath()
		} else {
			config, _ = projectstore.GlobalConfigPath()
		}
	}
	result := struct {
		Status      string `json:"status"`
		UUID        string `json:"uuid,omitempty"`
		ProjectRoot string `json:"project_root"`
		Marker      string `json:"marker"`
		Store       string `json:"store,omitempty"`
		ConfigScope string `json:"config_scope,omitempty"`
		Config      string `json:"config,omitempty"`
	}{string(ctx.Status), ctx.ID.String(), ctx.ProjectRoot, ctx.MarkerPath, ctx.StoreRoot, string(ctx.ConfigScope), config}
	if ctx.ID.String() == "00000000-0000-0000-0000-000000000000" {
		result.UUID = ""
	}
	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("Status: %s\nProject: %s\nMarker: %s\n", strings.ToUpper(result.Status), result.ProjectRoot, result.Marker)
	if result.Store != "" {
		fmt.Printf("UUID: %s\nStore: %s\nConfig scope: %s\nConfig: %s\n", result.UUID, result.Store, result.ConfigScope, result.Config)
	}
	return 0
}
