package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func handleConfigScopeCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: machtiani config scope <show|use> [flags]")
		return 2
	}
	switch args[0] {
	case "show":
		fs := pflag.NewFlagSet("machtiani config scope show", pflag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		jsonOutput := fs.Bool("json", false, "print scope as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return configUsageError("config scope show takes no positional arguments")
		}
		ctx, err := projectstore.Discover("")
		if err != nil {
			return configError(err)
		}
		if ctx.Status != projectstore.StatusInitialized {
			return configError(fmt.Errorf("project is not initialized; run machtiani init"))
		}
		if *jsonOutput {
			data, _ := json.Marshal(map[string]string{"config_scope": string(ctx.ConfigScope)})
			fmt.Println(string(data))
		} else {
			fmt.Println(ctx.ConfigScope)
		}
		return 0
	case "use":
		fs := pflag.NewFlagSet("machtiani config scope use", pflag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		noInteractive := fs.Bool("no-interactive", false, "never prompt")
		copyGlobal := fs.Bool("copy-global", false, "copy global config when enabling project scope")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			return configUsageError("config scope use requires global or project")
		}
		scope := projectstore.ConfigScope(fs.Arg(0))
		if err := scope.Validate(); err != nil {
			return configUsageError(err.Error())
		}
		ctx, err := projectstore.Discover("")
		if err != nil {
			return configError(err)
		}
		if ctx.Status != projectstore.StatusInitialized {
			return configError(fmt.Errorf("project is not initialized; run machtiani init"))
		}
		if !*noInteractive {
			confirmed, err := promptYesNo(bufio.NewReader(os.Stdin), os.Stdout, fmt.Sprintf("Use %s config for this project? [y/N]: ", scope), false)
			if err != nil || !confirmed {
				return configError(fmt.Errorf("configuration scope unchanged"))
			}
		}
		if scope == projectstore.ScopeProject {
			if _, err := os.Stat(ctx.ProjectConfigPath()); os.IsNotExist(err) {
				if !*copyGlobal {
					return configError(fmt.Errorf("project config does not exist; pass --copy-global or create it with --project"))
				}
				global, err := projectstore.GlobalConfigPath()
				if err != nil {
					return configError(err)
				}
				data, err := os.ReadFile(global)
				if err != nil {
					return configError(err)
				}
				if err := os.MkdirAll(filepath.Dir(ctx.ProjectConfigPath()), 0o700); err != nil {
					return configError(err)
				}
				if err := os.WriteFile(ctx.ProjectConfigPath(), data, 0o600); err != nil {
					return configError(err)
				}
			}
		}
		if err := projectstore.WriteConfigScope(ctx.StoreRoot, scope); err != nil {
			return configError(err)
		}
		fmt.Printf("Configuration scope: %s\n", scope)
		return 0
	default:
		return configUsageError("config scope must be show or use")
	}
}
