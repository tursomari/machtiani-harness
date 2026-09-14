package configfiles

import (
	"github.com/BurntSushi/toml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationAndCopyPreserveIndependentCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	old := filepath.Join(home, ".machtiani/config.toml")
	original := []byte("default_model = 'fixture'\n[providers.fixture]\napi_key = 'private-fixture'\nbase_url = 'https://example.test'\n[models.fixture]\nprovider = 'fixture'\nmodel = 'fixture'\n")
	if err := Write(old, original); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".config/machtiani/config.toml")
	if err := MigrateGlobal(target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-fixture") {
		t.Fatal("migration left literal key")
	}
	if got, _ := os.ReadFile(old); string(got) != string(original) {
		t.Fatal("legacy recovery copy changed")
	}
	copyPath := filepath.Join(home, "project/config.toml")
	if err := Import(target, copyPath, ""); err != nil {
		t.Fatal(err)
	}
	var copied map[string]any
	if err := toml.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(filepath.Dir(target), "credentials.env"), []byte("UNRELATED=changed-fixture\n")); err != nil {
		t.Fatal(err)
	}
	keys, err := Read(filepath.Join(filepath.Dir(copyPath), "credentials.env"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range keys {
		found = found || v == "private-fixture"
	}
	if !found {
		t.Fatal("copied config depends on source credentials")
	}
	if err := MigrateGlobal(target); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(target); string(again) != string(data) {
		t.Fatal("repeat migration changed destination")
	}
}

func TestCredentialsRejectUnsafeFilesAndDoNotExecuteContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.env")
	for _, content := range []string{"KEY=first\nKEY=second\n", "export KEY=secret-fixture\n", "KEY=secret fixture\n"} {
		if err := Write(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil || strings.Contains(err.Error(), "secret-fixture") {
			t.Fatal("unsafe assignment accepted or disclosed")
		}
	}
	if err := Write(path, []byte("KEY=$(id)\n")); err != nil {
		t.Fatal(err)
	}
	values, err := Read(path)
	if err != nil || values["KEY"] != "$(id)" {
		t.Fatal("file content must be data")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("public credential file accepted")
	}
	link := filepath.Join(dir, "linked.env")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := Write(link, []byte("KEY=replaced\n")); err == nil {
		t.Fatal("symlink overwrite accepted")
	}
}

func TestRejectedConfigDestinationDoesNotSaveCredential(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.toml")
	target := filepath.Join(dir, "config.toml")
	if err := Write(original, []byte("sentinel\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, target); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{"providers": map[string]any{"fixture": map[string]any{"api_key": "private-fixture"}}}
	if err := Save(target, raw); err == nil {
		t.Fatal("symlink config accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.env")); !os.IsNotExist(err) {
		t.Fatal("rejected config created credentials")
	}
	if data, _ := os.ReadFile(original); string(data) != "sentinel\n" {
		t.Fatal("original changed")
	}
}
