package transcript

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"
)


type Transcript struct {
    f    *os.File
    path string
    mem  strings.Builder
}

func New() (*Transcript, error) {
    if err := os.MkdirAll(".machtiani/chat", 0o755); err != nil {
        return nil, err
    }
    name := fmt.Sprintf("agent-%s.md", time.Now().UTC().Format("20060102T150405"))
    p := filepath.Join(".machtiani", "chat", name)
    f, err := os.Create(p)
    if err != nil { return nil, err }
    return &Transcript{f: f, path: p}, nil
}

func (t *Transcript) Path() string { return t.path }

func (t *Transcript) Close() error {
    if t.f == nil { return nil }
    return t.f.Close()
}

func (t *Transcript) WriteHeader(goal string, sessionID string, _ any) error {
    s := fmt.Sprintf("# mct-agent Transcript\n\nSession: %s\n\nGoal:\n%s\n\n", sessionID, goal)
    t.mem.WriteString(s)
    _, err := t.f.WriteString(s)
    return err
}

func (t *Transcript) WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error {
    var b strings.Builder
    b.WriteString(fmt.Sprintf("\n## Turn %d\n\n", step))
    b.WriteString("Question:\n")
    b.WriteString(question+"\n\n")
    if savedPath != "" {
        b.WriteString("mct chat: "+savedPath+"\n\n")
    }
    if len(retrieved) > 0 {
        b.WriteString("Retrieved File Paths:\n")
        for _, p := range retrieved { b.WriteString("- "+p+"\n") }
        b.WriteString("\n")
    }
    if summary != "" {
        b.WriteString("Answer:\n")
        b.WriteString(summary+"\n\n")
    }
    b.WriteString("Planner decision: ")
    b.WriteString(decision)
    b.WriteString("\n")
    s := b.String()
    t.mem.WriteString(s)
    _, err := t.f.WriteString(s)
    return err
}

func (t *Transcript) WriteFinal(answer string, step int, capped bool) error {
    var note string
    if capped {
        note = " (reached max-steps cap)"
    }
    s := fmt.Sprintf("\n## Conclusion%s (after %d turn(s))\n\n%s\n", note, step, answer)
    t.mem.WriteString(s)
    _, err := t.f.WriteString(s)
    return err
}

// Tail returns up to the last maxBytes of the transcript file.
func (t *Transcript) Tail(maxBytes int) (string, error) {
    if t == nil {
        return "", nil
    }
    data := t.mem.String()
    if maxBytes <= 0 || len(data) <= maxBytes {
        return data, nil
    }
    return data[len(data)-maxBytes:], nil
}

// Content returns the full in-memory transcript content.
func (t *Transcript) Content() string {
    if t == nil {
        return ""
    }
    return t.mem.String()
}
