package main

import (
    "fmt"
    "log"

    patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
)

func main() {
    const sessionID = "agent-20251208T231210-6114"
    const repoRoot = "/home/david/projects/undici"

    baseline, err := patchersvc.LoadBaseline(sessionID)
    if err != nil {
        log.Fatalf("load baseline: %v", err)
    }

    section, included, err := patchersvc.BuildBaselineDiffSection(baseline, repoRoot, "LICENSE")
    if err != nil {
        log.Fatalf("build baseline diff: %v", err)
    }

    fmt.Printf("included=%v\n", included)
    fmt.Println(section)
}

