package utils

import (
	"os"

	"github.com/sirupsen/logrus"
)

// Log is the shared logger used throughout the agent.
// Infrastructure-level logs (e.g., workspace sync operations) are filtered
// to prevent context pollution in the MCT-Agent chat context.
var Log = func() *logrus.Logger {
	logger := logrus.New()

	// Wrap stdout with filter to exclude infrastructure logs that should
	// not appear in the MCT-Agent chat context
	filterWriter := NewLogFilterWriter(
		os.Stdout,
		"filtered workspace sync start",
		"filtered workspace sync complete",
	)

	logger.SetOutput(filterWriter)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	return logger
}()
