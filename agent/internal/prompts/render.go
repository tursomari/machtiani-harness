package prompts

import (
	"fmt"
	"strings"
	"text/template"
)

var defaultFuncMap = template.FuncMap{
	"trim": strings.TrimSpace,
	"join": strings.Join,
}

// Render renders a Go template string with the provided data and an optional function map.
func Render(name string, tmpl string, data any, funcs template.FuncMap) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return "", fmt.Errorf("template %s is empty", name)
	}
	t := template.New(name).Option("missingkey=error")
	if funcs == nil {
		funcs = defaultFuncMap
	} else if len(funcs) > 0 {
		funcs = mergeFuncMaps(defaultFuncMap, funcs)
	} else {
		funcs = defaultFuncMap
	}
	t = t.Funcs(funcs)
	parsed, err := t.Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", name, err)
	}

	var b strings.Builder
	if err := parsed.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render template %s: %w", name, err)
	}
	return b.String(), nil
}

func mergeFuncMaps(base template.FuncMap, extras template.FuncMap) template.FuncMap {
	if len(extras) == 0 {
		return base
	}
	merged := make(template.FuncMap, len(base)+len(extras))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extras {
		merged[k] = v
	}
	return merged
}

// MustRender renders a template and panics on error. Intended for use in tests.
func MustRender(name string, tmpl string, data any, funcs template.FuncMap) string {
	rendered, err := Render(name, tmpl, data, funcs)
	if err != nil {
		panic(err)
	}
	return rendered
}
