package prompts

import (
	"strings"
	"testing"
	"text/template"
)

func TestRender(t *testing.T) {
	tmpl := "Hello, {{.Name}}!"
	got, err := Render("greeting", tmpl, map[string]string{"Name": "World"}, nil)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if want := "Hello, World!"; got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderMissingKey(t *testing.T) {
	tmpl := "Hello, {{.Name}}!"
	if _, err := Render("greeting", tmpl, map[string]string{}, nil); err == nil {
		t.Fatalf("expected error for missing key")
	}
}

func TestMergeFuncMaps(t *testing.T) {
	extra := template.FuncMap{"upper": strings.ToUpper}
	merged := mergeFuncMaps(defaultFuncMap, extra)
	if _, ok := merged["join"]; !ok {
		t.Fatalf("expected join to be present")
	}
	if _, ok := merged["upper"]; !ok {
		t.Fatalf("expected upper to be present")
	}
}
