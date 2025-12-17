package main

import (
	"fmt"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func main() {
	cfg, path, err := llm.LoadGlobalConfig()
	fmt.Println("path:", path)
	fmt.Println("err:", err)
	fmt.Printf("workspace nil? %v\n", cfg.Workspace == nil)
	if cfg.Workspace != nil {
		fmt.Printf("workspace: %+v\n", *cfg.Workspace)
	}
}
