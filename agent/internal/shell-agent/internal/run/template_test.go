package run

import (
	"strings"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		vars    map[string]interface{}
		want    string
		wantErr string
	}{
		{
			name: "simple render",
			tmpl: "Hello, {{.Name}}!",
			vars: map[string]interface{}{"Name": "world"},
			want: "Hello, world!",
		},
		{
			name:    "missing variable",
			tmpl:    "{{.Missing}}",
			vars:    map[string]interface{}{},
			wantErr: "map has no entry for key \"Missing\"",
		},
		{
			name:    "parse error",
			tmpl:    "{{#}}",
			vars:    map[string]interface{}{},
			wantErr: "template parse",
		},
		{
			name:    "type mismatch",
			tmpl:    "{{range .Items}}{{.}}{{end}}",
			vars:    map[string]interface{}{"Items": "not a slice"},
			wantErr: "range can't iterate",
		},
		{
			name: "complex template",
			tmpl: `{{if .Enabled}}{{range $i, $v := .Items}}{{$i}}:{{$v}};{{end}}{{else}}disabled{{end}}`,
			vars: map[string]interface{}{"Enabled": true, "Items": []string{"a", "b"}},
			want: "0:a;1:b;",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderTemplate(tc.tmpl, tc.vars)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderTemplate error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("RenderTemplate() = %q, want %q", got, tc.want)
			}
		})
	}
}

type flattenSample struct {
	Timeout int
	Nested  nestedSample
}

type nestedSample struct {
	Path string
}

func TestFlattenConfigMergesValues(t *testing.T) {
	first := flattenSample{Timeout: 30, Nested: nestedSample{Path: "/tmp"}}
	second := struct {
		Timeout int
		Extra   string
	}{Timeout: 45, Extra: "value"}

	got := FlattenConfig(first, second)
	if got["Timeout"].(float64) != 45 {
		t.Fatalf("Timeout = %v, want 45", got["Timeout"])
	}
	nested, ok := got["Nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("Nested = %v, want map", got["Nested"])
	}
	if nested["Path"].(string) != "/tmp" {
		t.Fatalf("Nested.Path = %v, want /tmp", nested["Path"])
	}
	if got["Extra"].(string) != "value" {
		t.Fatalf("Extra = %v, want value", got["Extra"])
	}
}

func TestFlattenConfigSkipsNilAndErrors(t *testing.T) {
	invalid := struct {
		Callback func()
	}{Callback: func() {}}

	got := FlattenConfig(nil, invalid)
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %#v", got)
	}
}
