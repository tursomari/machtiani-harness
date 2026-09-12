package shellagent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestBuildLibraryUsesSelectedProviderCredential(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configKey  string
		openrouter string
		openai     string
		overrides  map[string]string
		wantKey    string
	}{
		{"environment reference", "${DEEPSEEK_API_KEY}", "test-openrouter", "test-openai", nil, "test-deepseek"},
		{"OpenAI environment only", "${DEEPSEEK_API_KEY}", "", "test-openai", nil, "test-deepseek"},
		{"literal credential", "test-configured", "test-openrouter", "test-openai", nil, "test-configured"},
		{"provider environment fallback", "", "test-openrouter", "test-openai", nil, "test-deepseek"},
		{"explicit provider override", "${DEEPSEEK_API_KEY}", "test-openrouter", "test-openai", map[string]string{"deepseek": "test-override"}, "test-override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetConfigForTesting()
			t.Cleanup(llm.ResetConfigForTesting)
			t.Setenv("MACHTIANI_LLM_TEST_STUB", "")
			t.Setenv("DEEPSEEK_API_KEY", "test-deepseek")
			t.Setenv("OPENROUTER_API_KEY", tc.openrouter)
			t.Setenv("OPENAI_API_KEY", tc.openai)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+tc.wantKey {
					t.Error("request did not use the selected provider's credential")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			t.Cleanup(server.Close)

			configPath := filepath.Join(t.TempDir(), "config.toml")
			config := fmt.Sprintf(`default_model = "worker"
[providers.deepseek]
base_url = %q
api_key = %q
[models.worker]
provider = "deepseek"
model = "test-model"
`, server.URL, tc.configKey)
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MACHTIANI_CONFIG", configPath)
			global, _, err := llm.LoadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			library, err := BuildLibrary(&global, tc.overrides, false, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if closer, ok := library.Env.(interface{ Close() error }); ok {
				t.Cleanup(func() { _ = closer.Close() })
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := library.Model.Query(ctx, []minisweagent.Message{{Role: "user", Content: "hello"}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != "ok" {
				t.Fatalf("response = %q, want ok", result.Content)
			}
		})
	}
}
