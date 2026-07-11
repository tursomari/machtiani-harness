package prompt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/shellbridge"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

// runShellAgentWithInterception invokes shellagent.Run while intercepting
// lines written to os.Stdout. Lines prefixed with shellbridge.ActionPrefix are
// parsed as JSON and emitted as trajectory events (kind "shell-agent.action");
// all other lines are forwarded to the original stdout unchanged.
func runShellAgentWithInterception(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
	original := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		return shellagent.Result{}, fmt.Errorf("create stdout pipe: %w", err)
	}

	os.Stdout = w

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		// Allow long lines (up to 1 MiB) so large action payloads are not
		// silently truncated.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, shellbridge.ActionPrefix) {
				rest := strings.TrimPrefix(line, shellbridge.ActionPrefix)
				var payload map[string]any
				if err := json.Unmarshal([]byte(rest), &payload); err != nil {
					fmt.Fprintf(original, "intercept: parse action json: %v\n", err)
					continue
				}
				evt := trajectory.Event{
					Kind:    "shell-agent.action",
					Payload: payload,
				}
				if err := trajectory.EmitFromContext(ctx, evt); err != nil {
					fmt.Fprintf(original, "intercept: emit trajectory event: %v\n", err)
				}
				continue
			}
			fmt.Fprintf(original, "%s\n", line)
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			fmt.Fprintf(original, "intercept: scanner: %v\n", err)
		}
	}()

	res, runErr := shellagent.Run(ctx, req)

	os.Stdout = original
	_ = w.Close()
	wg.Wait()

	return res, runErr
}
