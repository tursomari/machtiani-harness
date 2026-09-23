package shellagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type recoveryModel struct {
	perRunCountingModel
	responses  []string
	failure    error
	queries    [][]minisweagent.Message
	afterQuery func(int)
}

func (m *recoveryModel) Query(_ context.Context, msgs []minisweagent.Message, _ ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.queries = append(m.queries, append([]minisweagent.Message(nil), msgs...))
	if m.nCalls >= len(m.responses) {
		if m.failure != nil {
			return minisweagent.QueryResult{}, m.failure
		}
		return minisweagent.QueryResult{}, errors.New("unexpected extra model call")
	}
	content := m.responses[m.nCalls]
	m.nCalls++
	if m.afterQuery != nil {
		m.afterQuery(m.nCalls)
	}
	return minisweagent.QueryResult{Content: content}, nil
}

type recoveryEnv struct {
	stubEnvForCounting
	commands []string
}

func (e *recoveryEnv) Execute(_ context.Context, command, _ string) (minisweagent.ExecuteResult, error) {
	e.commands = append(e.commands, command)
	return minisweagent.ExecuteResult{Output: "completed action", ReturnCode: 0}, nil
}

func recoveryRequest(t *testing.T, model *recoveryModel, env *recoveryEnv) Request {
	t.Helper()
	return Request{
		Task:                   "complete the task",
		PreconstructedMessages: []llm.Message{{Role: "system", Content: "Use <command> or <answer>."}, {Role: "user", Content: "complete the task"}},
		Config:                 &minisweagent.ShellAgentConfig{}, Prompts: &minisweagent.PromptsConfig{}, Model: model, Env: env,
		TrajectoryBaseDir: t.TempDir(),
	}
}

func TestFormatRecoveryStartsFreshAndPreservesExecutedWork(t *testing.T) {
	m := &recoveryModel{responses: []string{"<command>echo done</command>", "malformed-1", "malformed-2", "malformed-3", "<answer>Recovered answer</answer>"}}
	e := &recoveryEnv{}
	req := recoveryRequest(t, m, e)
	res, err := Run(context.Background(), req)
	if err != nil || res.Error != nil || res.ExitStatus != "Submitted" || res.Answer != "Recovered answer" || !res.Restarted {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if len(e.commands) != 1 || m.nCalls != 5 {
		t.Fatalf("commands = %v, calls = %d", e.commands, m.nCalls)
	}
	redo := m.queries[4]
	if len(redo) != 3 || redo[1].Content != req.Task || !hasRecoveryContext(redo) {
		t.Fatalf("redo history = %+v", redo)
	}
	if !strings.Contains(redo[2].Content, "Command executed: echo done") || !strings.Contains(redo[2].Content, "completed action") {
		t.Fatal("missing prior execution observations")
	}
	for _, msg := range redo {
		if strings.Contains(msg.Content, "malformed-") {
			t.Fatal("replayed rejected assistant response")
		}
	}
	archive, err := os.ReadFile(filepath.Join(req.TrajectoryBaseDir, "format-error-attempt-1.json"))
	if err != nil || !strings.Contains(string(archive), "malformed-3") || !strings.Contains(string(archive), "FormatErrorLoop") {
		t.Fatalf("failed attempt not archived: %v", err)
	}
}

func TestFormatRecoveryExhaustedAndResumeCannotGrantAnotherRedo(t *testing.T) {
	m := &recoveryModel{responses: []string{"bad", "bad", "bad", "bad", "bad", "bad"}}
	e := &recoveryEnv{}
	req := recoveryRequest(t, m, e)
	for i := 0; i < 2; i++ {
		res, err := Run(context.Background(), req)
		var failure *Failure
		if err != nil || !errors.As(res.Error, &failure) || failure.Attempts != 2 || failure.Code != "FormatErrorLoop" {
			t.Fatalf("result = %+v, err = %v", res, err)
		}
		if m.nCalls != 6 || len(e.commands) != 0 {
			t.Fatalf("calls = %d, commands = %v", m.nCalls, e.commands)
		}
		req.ResumeAttempt = true
	}
}

func TestFormatRecoveryCancellationDoesNotStartRedo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &recoveryModel{responses: []string{"bad", "bad", "bad"}, afterQuery: func(n int) {
		if n == 3 {
			cancel()
		}
	}}
	req := recoveryRequest(t, m, &recoveryEnv{})
	res, err := Run(ctx, req)
	if !errors.Is(err, context.Canceled) || res.Restarted || m.nCalls != 3 {
		t.Fatalf("result = %+v, err = %v, calls = %d", res, err, m.nCalls)
	}
}

func TestFormatRecoveryInterruptedRedoRetainsBudgetOnResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &recoveryModel{responses: []string{"bad", "bad", "bad", "bad", "bad", "bad"}, afterQuery: func(n int) {
		if n == 4 {
			cancel()
		}
	}}
	req := recoveryRequest(t, m, &recoveryEnv{})
	res, err := Run(ctx, req)
	if err != nil || !errors.Is(res.Error, context.Canceled) || !res.Restarted {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	req.ResumeAttempt = true
	res, err = Run(context.Background(), req)
	var failure *Failure
	if err != nil || !errors.As(res.Error, &failure) || m.nCalls != 6 {
		t.Fatalf("result = %+v, err = %v, calls = %d", res, err, m.nCalls)
	}
}

func TestFormatRecoveryReportsProviderErrorDuringRedo(t *testing.T) {
	m := &recoveryModel{responses: []string{"bad", "bad", "bad"}}
	req := recoveryRequest(t, m, &recoveryEnv{})
	res, err := Run(context.Background(), req)
	var failure *Failure
	if err != nil || !errors.As(res.Error, &failure) || failure.Code != "Error" || !strings.Contains(failure.Diagnostic, "unexpected extra model call") {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
}

func TestFormatRecoveryResumesAfterFirstAttemptStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &recoveryModel{responses: []string{"bad", "bad", "bad", "<answer>Recovered</answer>"}, afterQuery: func(n int) {
		if n == 3 {
			cancel()
		}
	}}
	req := recoveryRequest(t, m, &recoveryEnv{})
	if _, err := Run(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	req.ResumeAttempt = true
	res, err := Run(context.Background(), req)
	if err != nil || res.Error != nil || !res.Restarted || res.Answer != "Recovered" || m.nCalls != 4 {
		t.Fatalf("result = %+v, err = %v, calls = %d", res, err, m.nCalls)
	}
}

func TestModelHostFailureBecomesStructuredWorkFailure(t *testing.T) {
	for _, code := range []string{"TRANSIENT_ERROR", "EMPTY_RESPONSE", "AUTH_REQUIRED"} {
		t.Run(code, func(t *testing.T) {
			m := &recoveryModel{responses: []string{"<command>echo done</command>"}, failure: &llm.ModelHostCallError{Code: code, Message: "provider explanation", Attempts: 3}}
			e := &recoveryEnv{}
			res, err := Run(context.Background(), recoveryRequest(t, m, e))
			var failure *Failure
			if err != nil || !errors.As(res.Error, &failure) || failure.Code != code || failure.Attempts != 3 || !strings.Contains(failure.Diagnostic, "provider explanation") {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if res.Restarted || len(e.commands) != 1 || len(m.queries) != 2 {
				t.Fatalf("replayed work: result=%+v commands=%v calls=%d", res, e.commands, len(m.queries))
			}
			data, err := os.ReadFile(res.TrajectoryPath)
			if err != nil || !strings.Contains(string(data), "echo done") {
				t.Fatalf("trajectory missing: %v", err)
			}
		})
	}
}
