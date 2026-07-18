package presentation

import (
	"fmt"
	"os"
	"strings"
)

// MotionMode controls terminal animation independently of semantic styling.
type MotionMode string

const (
	MotionFull    MotionMode = "full"
	MotionReduced MotionMode = "reduced"
	MotionNone    MotionMode = "none"
)

func NormalizeMotionMode(value string) (MotionMode, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return MotionFull, nil
	}
	switch MotionMode(value) {
	case MotionFull, MotionReduced, MotionNone:
		return MotionMode(value), nil
	default:
		return "", fmt.Errorf("unknown UI motion mode %q (want full, reduced, or none)", value)
	}
}

func ResolveMotionMode(configured string) (MotionMode, error) {
	if override := strings.TrimSpace(os.Getenv("MACHTIANI_MOTION")); override != "" {
		return NormalizeMotionMode(override)
	}
	return NormalizeMotionMode(configured)
}
