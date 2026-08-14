package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	updatepkg "github.com/tursomari/machtiani/agent/internal/update"
)

type fakeUpdateManager struct {
	result    updatepkg.Result
	checkErr  error
	updated   bool
	installed bool
	source    string
	prefix    string
}

func (f *fakeUpdateManager) Check(context.Context) (updatepkg.Result, error) {
	return f.result, f.checkErr
}

func (f *fakeUpdateManager) Update(context.Context, updatepkg.Result) (updatepkg.Result, error) {
	f.updated = true
	f.result.Status = updatepkg.StatusUpdated
	return f.result, nil
}

func (f *fakeUpdateManager) Install(_ context.Context, source, prefix string) (updatepkg.Receipt, error) {
	f.installed = true
	f.source = source
	f.prefix = prefix
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
	if fake.source != "/tmp/source" || fake.prefix != "/tmp/prefix" {
		t.Fatalf("Install() source=%q prefix=%q", fake.source, fake.prefix)
	}
}

func TestInstallNoInteractiveUsesDefaultPrefix(t *testing.T) {
	fake := &fakeUpdateManager{}
	restore := replaceUpdateManagerForTest(fake)
	defer restore()

	if code := handleInstallCommand([]string{"--source", "/tmp/source", "--no-interactive"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".local"); fake.prefix != want {
		t.Fatalf("default prefix = %q, want %q", fake.prefix, want)
	}
}

func TestResolveInstallPrefixSkipsPromptWhenDisabled(t *testing.T) {
	tests := []struct {
		name                               string
		prefixExplicit, noInteractive, tty bool
	}{
		{name: "explicit prefix", prefixExplicit: true, tty: true},
		{name: "no interactive flag", noInteractive: true, tty: true},
		{name: "redirected input"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := resolveInstallPrefix(
				"/home/demo/.local",
				"/home/demo",
				test.prefixExplicit,
				test.noInteractive,
				test.tty,
				bufio.NewReader(strings.NewReader("2\n")),
				&out,
				func(string) bool { return true },
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != "/home/demo/.local" {
				t.Fatalf("prefix = %q", got)
			}
			if out.Len() != 0 {
				t.Fatalf("selection prompted: %q", out.String())
			}
		})
	}
}

func TestAvailableInstallPrefixesIncludesTwoExistingAlternatives(t *testing.T) {
	home := "/home/demo"
	existing := map[string]bool{
		filepath.Join(home, "bin"):            true,
		filepath.Join("/opt/homebrew", "bin"): true,
		filepath.Join("/usr/local", "bin"):    true,
	}
	got := availableInstallPrefixes(filepath.Join(home, ".local"), home, func(path string) bool {
		return existing[path]
	})
	want := []string{filepath.Join(home, ".local"), home, "/opt/homebrew"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("availableInstallPrefixes() = %#v, want %#v", got, want)
	}
}

func TestPromptInstallPrefixShowsDestinationsAndExpandsCustomHome(t *testing.T) {
	var out bytes.Buffer
	got, err := promptInstallPrefix(
		bufio.NewReader(strings.NewReader("3\n~/tools\n")),
		&out,
		[]string{"/home/demo/.local", "/usr/local"},
		"/home/demo",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/demo/tools" {
		t.Fatalf("prefix = %q", got)
	}
	for _, want := range []string{"~/.local/bin/mct-agent (recommended)", "/usr/local/bin/mct-agent", "Custom prefix"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt output missing %q:\n%s", want, out.String())
		}
	}
}

func TestPrintUpdateSummaryWarnsOnDivergentBinary(t *testing.T) {
	fake := &fakeUpdateManager{result: updatepkg.Result{
		Status:             updatepkg.StatusCurrent,
		CurrentCommit:      strings.Repeat("a", 40),
		CandidateCommit:    strings.Repeat("a", 40),
		Remote:             "file:///tmp/remote.git",
		InstalledDivergent: true,
	}}
	restore := replaceUpdateManagerForTest(fake)
	defer restore()

	_, stderr := captureOutput(func() {
		if code := handleUpdateCommand([]string{"--check"}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
	// RED: current code prints "mct-agent is current at ..." even when the
	// binary on disk is divergent. The fixed code must warn instead.
	if !strings.Contains(stderr, "stale") && !strings.Contains(stderr, "divergent") && !strings.Contains(stderr, "not current") {
		t.Fatalf("expected divergence warning for stale binary, got: %s", stderr)
	}
	if strings.Contains(stderr, "mct-agent is current at") {
		t.Fatalf("must not report 'current' when binary is divergent, got: %s", stderr)
	}
}

func TestAutomaticUpdateEligibilityProtectsMachineOutput(t *testing.T) {
	tests := []struct {
		name                  string
		args                  []string
		inTTY, outTTY, errTTY bool
		want                  bool
	}{
		{name: "interactive command", args: []string{"run", "-p", "hello"}, inTTY: true, outTTY: true, errTTY: true, want: true},
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
