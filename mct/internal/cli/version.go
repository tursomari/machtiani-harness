package cli

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
	Dirty   = "unknown"
)

func printVersion() {
	fmt.Printf("mct %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
}
