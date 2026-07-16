package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	updatepkg "github.com/tursomari/machtiani/agent/internal/update"
)

type fakeUpdateManager struct {
	result    updatepkg.Result
	checkErr  error
	updated   bool
	installed bool
}

func (f *fakeUpdateManager) Check(context.Context) (updatepkg.Result, error) {
	return f.result, f.checkErr
}

func (f *fakeUpdateManager) Update(context.Context, updatepkg.Result) (updatepkg.Result, error) {
	f.updated = true
	f.result.Status = updatepkg.StatusUpdated
	return f.result, nil
}

func (f *fakeUpdateManager) Install(context.Context, string, string) (updatepkg.Receipt, error) {
	f.installed = true
	return updatepkg.Receipt{}, nil
}

func TestUpdateCheckJSONIsMachineSafe(t *testing.T) {
	fake := &fakeUpdateManager{result: updatepkg.Result{
		Status:          updatepkg.StatusAvailable,
		CurrentCommit:   strings.Repeat("a", 40),
		CandidateCommit: strings.Repeat("b", 40),
		Remote:          "file:///tmp/remote.git",
	}}
	restore := replaceUpdateManagerForTest(fake)
	defer restore()

	stdout, stderr := captureOutput(func() {
		if code := handleUpdateCommand([]string{"--check", "--json"}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var got updatepkg.Result
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.Status != updatepkg.StatusAvailable || fake.updated {
		t.Fatalf("result=%#v updated=%v", got, fake.updated)
	}
}

func TestUpdateNoInteractiveRequiresYesToMutate(t *testing.T) {
	fake := &fakeUpdateManager{result: updatepkg.Result{Status: updatepkg.StatusAvailable}}
	restore := replaceUpdateManagerForTest(fake)
	defer restore()
	if code := handleUpdateCommand([]string{"--no-interactive"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if fake.updated {
		t.Fatal("non-interactive check mutated installation without --yes")
	}
	if code := handleUpdateCommand([]string{"--no-interactive", "--yes"}); code != 0 {
		t.Fatalf("exit code with --yes = %d", code)
	}
	if !fake.updated {
		t.Fatal("--yes did not update")
	}
}

func TestInstallDelegatesToManagedInstallation(t *testing.T) {
	fake := &fakeUpdateManager{}
	restore := replaceUpdateManagerForTest(fake)
	defer restore()
	if code := handleInstallCommand([]string{"--source", "/tmp/source", "--prefix", "/tmp/prefix"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !fake.installed {
		t.Fatal("installation was not invoked")
	}
}

func TestAutomaticUpdateEligibilityProtectsMachineOutput(t *testing.T) {
	tests := []struct {
		name                  string
		args                  []string
		inTTY, outTTY, errTTY bool
		want                  bool
	}{
		{name: "interactive command", args: []string{"run", "-t", "hello"}, inTTY: true, outTTY: true, errTTY: true, want: true},
		{name: "stdout pipe", args: []string{"project", "show"}, inTTY: true, outTTY: false, errTTY: true},
		{name: "json", args: []string{"project", "show", "--json"}, inTTY: true, outTTY: true, errTTY: true},
		{name: "noninteractive", args: []string{"init", "--no-interactive"}, inTTY: true, outTTY: true, errTTY: true},
		{name: "version", args: []string{"--version"}, inTTY: true, outTTY: true, errTTY: true},
		{name: "update command", args: []string{"update"}, inTTY: true, outTTY: true, errTTY: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := automaticUpdateEligible(tc.args, tc.inTTY, tc.outTTY, tc.errTTY); got != tc.want {
				t.Fatalf("automaticUpdateEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

func replaceUpdateManagerForTest(fake updateCommandManager) func() {
	original := updateManagerFactory
	updateManagerFactory = func() updateCommandManager { return fake }
	return func() { updateManagerFactory = original }
}
