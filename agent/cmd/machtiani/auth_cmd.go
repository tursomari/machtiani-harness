package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

type modelHostProtocolResponse struct {
	Version int             `json:"v"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func handleAuthCommand(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: machtiani auth <status|login|logout> [--model <alias>]")
		return 0
	}
	action := args[0]
	if action != "status" && action != "login" && action != "logout" {
		fmt.Fprintf(os.Stderr, "Unknown auth subcommand: %s\n", action)
		return 2
	}
	flags := pflag.NewFlagSet("machtiani auth "+action, pflag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	model := flags.String("model", "", "model alias (defaults to default_model)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if len(flags.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "auth accepts no positional arguments after the action")
		return 2
	}
	resolved, err := llm.ResolveModel(*model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auth configuration error: %v\n", err)
		return 1
	}
	if resolved.Transport != "model-host" {
		fmt.Fprintln(os.Stderr, "This model does not use the shared model host; manage its configured API credential directly.")
		return 1
	}
	result, protocolErr, err := callModelHostAuth(resolved.Command, resolved.Profile, "auth/"+action)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Authentication command failed: %v\n", err)
		return 1
	}
	if protocolErr != nil {
		fmt.Fprintf(os.Stderr, "Authentication %s: %s (%s)\n", action, protocolErr.Message, protocolErr.Code)
		return 1
	}
	if action == "status" {
		var status struct {
			Authenticated bool   `json:"authenticated"`
			Method        string `json:"method"`
		}
		if err := json.Unmarshal(result, &status); err != nil {
			fmt.Fprintln(os.Stderr, "Authentication status returned an invalid response.")
			return 1
		}
		state := "not authenticated"
		if status.Authenticated {
			state = "authenticated"
		}
		fmt.Printf("%s: %s (%s)\n", resolved.ProviderName, state, status.Method)
		return 0
	}
	fmt.Printf("Authentication %s completed for %s.\n", action, resolved.ProviderName)
	return 0
}

func callModelHostAuth(command, profile, method string) (json.RawMessage, *struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = "machtiani-model-host"
	}
	process := exec.Command(command, "--profile", profile)
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	process.Stderr = os.Stderr
	if err := process.Start(); err != nil {
		return nil, nil, err
	}
	request := map[string]any{"v": 1, "id": "auth", "method": method}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		_ = process.Process.Kill()
		return nil, nil, err
	}
	if err := stdin.Close(); err != nil {
		_ = process.Process.Kill()
		return nil, nil, err
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		waitErr := process.Wait()
		if scanner.Err() != nil {
			return nil, nil, scanner.Err()
		}
		if waitErr != nil {
			return nil, nil, fmt.Errorf("model host exited before responding")
		}
		return nil, nil, fmt.Errorf("model host returned no response")
	}
	line := append([]byte(nil), scanner.Bytes()...)
	if err := process.Wait(); err != nil {
		return nil, nil, fmt.Errorf("model host exited unsuccessfully")
	}
	var response modelHostProtocolResponse
	if err := json.Unmarshal(line, &response); err != nil || response.Version != 1 || response.ID != "auth" {
		return nil, nil, fmt.Errorf("model host returned an invalid response")
	}
	return response.Result, response.Error, nil
}
