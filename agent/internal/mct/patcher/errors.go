package patcher

import "fmt"

// ValidationError represents invalid instructions or conflicts detected prior to
// generating a patch. Callers should surface the wrapped error message to the
// user.
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("validation error: %v", e.Err)
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PatchNotCleanError indicates the generated patch does not apply cleanly to
// the repository. Diagnostics contain parsed git apply output for display.
type PatchNotCleanError struct {
	Err         error
	Diagnostics PatchValidationDiagnostics
}

func (e *PatchNotCleanError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("patch does not apply cleanly: %v", e.Err)
}

func (e *PatchNotCleanError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PatchGenerationError wraps unexpected errors that occur while creating the
// patch diff or writing the resulting file.
type PatchGenerationError struct {
	Err error
}

func (e *PatchGenerationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("patch generation error: %v", e.Err)
}

func (e *PatchGenerationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
