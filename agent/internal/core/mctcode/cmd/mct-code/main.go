package main

import (
	"flag"
	"fmt"
	"os"

	mctcode "github.com/tursomari/machtiani/agent/internal/core/mctcode"
)

func main() {
	verbose := flag.Bool("verbose", false, "print tool dispatches and results to stderr")
	flag.Parse()

	// Fallback env var
	if !*verbose && os.Getenv("MCT_VERBOSE") == "1" {
		*verbose = true
	}

	// Consume the flag package args before passing remainder to Run.
	// flag.Parse leaves non-flag args in flag.Args().
	os.Args = append([]string{os.Args[0]}, flag.Args()...)

	if err := mctcode.Run(*verbose); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
