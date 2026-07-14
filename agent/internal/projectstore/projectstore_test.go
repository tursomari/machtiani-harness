package projectstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestDiscoverInitializedGitProject(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	gitInit(t, repo)
	id := uuid.New()
	if err := WriteProjectUUID(repo, id); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(home, RootDirName, id.String())
	if err := EnsureLayout(store); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigScope(store, ScopeProject); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, err := Discover(nested)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != StatusInitialized || ctx.ID != id || ctx.StoreRoot != store {
		t.Fatalf("unexpected context: %+v", ctx)
	}
	if ctx.ConfigScope != ScopeProject {
		t.Fatalf("scope = %q", ctx.ConfigScope)
	}
}

func TestDiscoverLegacyAndCleanProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clean := t.TempDir()
	ctx, err := Discover(clean)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != StatusUninitialized {
		t.Fatalf("clean status = %q", ctx.Status)
	}
	examples := t.TempDir()
	for _, name := range []string{"config.minimal.toml", "config.comprehensive.toml"} {
		path := filepath.Join(examples, RootDirName, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# example\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, err = Discover(examples)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != StatusUninitialized {
		t.Fatalf("tracked examples status = %q", ctx.Status)
	}
	legacy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacy, RootDirName, SessionsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, err = Discover(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != StatusLegacy {
		t.Fatalf("legacy status = %q", ctx.Status)
	}
}

func TestReadProjectUUIDRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", uuid.NewString() + " extra", uuid.Nil.String()} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			path := MarkerPath(root)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadProjectUUID(root); err == nil {
				t.Fatal("expected invalid UUID error")
			}
		})
	}
}

func TestNonGitMarkerSearchesParents(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	id := uuid.New()
	if err := WriteProjectUUID(root, id); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, err := Discover(nested)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ProjectRoot != root || ctx.ID != id {
		t.Fatalf("unexpected context: %+v", ctx)
	}
}

func TestConfigScopeRoundTripAndValidation(t *testing.T) {
	store := t.TempDir()
	if err := WriteConfigScope(store, ScopeProject); err != nil {
		t.Fatal(err)
	}
	scope, err := ReadConfigScope(store)
	if err != nil {
		t.Fatal(err)
	}
	if scope != ScopeProject {
		t.Fatalf("scope = %q", scope)
	}
	if err := WriteConfigScope(store, ConfigScope("bad")); err == nil {
		t.Fatal("expected invalid scope error")
	}
}

func TestEnsureLayoutUsesPrivatePermissions(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	if err := EnsureLayout(store); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", SessionsDirName, ArtifactsDirName, ScratchDirName, MetaDirName} {
		info, err := os.Stat(filepath.Join(store, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s permissions = %o", name, info.Mode().Perm())
		}
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--quiet", dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
}
