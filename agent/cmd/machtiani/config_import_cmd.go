package main

import (
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/configfiles"
	"os"
)

// Import is an explicit, non-overwriting migration into the selected config.
func handleConfigImport(args []string) int {
	fs := pflag.NewFlagSet("machtiani config import", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	source := fs.String("source", "", "configuration to copy without modifying it")
	credentials := fs.String("credentials-file", "", "private credential source for legacy environment references")
	if err := fs.Parse(args); err != nil {
		return configUsageError(err.Error())
	}
	if *source == "" || fs.NArg() != 0 {
		return configUsageError("config import requires --source")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	if err := configfiles.Import(*source, target.path, *credentials); err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	return 0
}
