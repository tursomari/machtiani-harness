package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
    "regexp"
    "strings"
)

type chatReq struct {
    Model    string `json:"model"`
    Stream   bool   `json:"stream"`
    Messages []struct {
        Role    string `json:"role"`
        Content string `json:"content"`
    } `json:"messages"`
}

type chatResp struct {
    Choices []struct {
        Message struct {
            Content string `json:"content"`
        } `json:"message"`
    } `json:"choices"`
}

var stepRe = regexp.MustCompile(`(?i)Step\s+(\d+)\s+of\s+(\d+)`)

func main() {
    port := flag.Int("port", 53100, "listen port")
    flag.Parse()

    mux := http.NewServeMux()
    mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
        defer r.Body.Close()
        var req chatReq
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
            return
        }
        content := ""
        if len(req.Messages) > 0 {
            content = req.Messages[len(req.Messages)-1].Content
        }

        var reply string
        lower := strings.ToLower(content)
        if strings.Contains(lower, "composer agent") || strings.Contains(lower, "final answer") {
            reply = "This is a final answer from the mock OpenAI server based on the transcript."
        } else if strings.Contains(lower, "decide") {
            step, max := 1, 1
            if m := stepRe.FindStringSubmatch(content); len(m) == 3 {
                fmt.Sscanf(m[1], "%d", &step)
                fmt.Sscanf(m[2], "%d", &max)
            }
            if step == 2 && max >= 3 {
                reply = "Decision: patch\n{\n  \"metadata\": { \"description\": \"Apply test patch\" },\n  \"edits\": []\n}"
            } else if step < max {
                reply = "Decision: ask\nQuestion: Please examine README.md for high-signal repository context."
            } else {
                reply = "Decision: finalize"
            }
        } else {
            reply = "Decision: finalize"
        }

        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(chatResp{Choices: []struct{ Message struct{ Content string "json:\"content\"" } "json:\"message\"" }{{Message: struct{ Content string "json:\"content\"" }{Content: reply}}}})
    })

    addr := fmt.Sprintf(":%d", *port)
    log.Printf("mock-openai listening on %s", addr)
    log.Fatal(http.ListenAndServe(addr, mux))
}
