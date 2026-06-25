package main

import (
	"fmt"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func main() {
	cfg, path, err := llm.LoadGlobalConfig()
	fmt.Println("path:", path)
	fmt.Println("err:", err)
	fmt.Printf("default_model: %s\n", cfg.DefaultModel)
}
