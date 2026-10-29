// Package errs defines the error classes shared across md2pdf packages.
package errs

import (
	"errors"
	"fmt"
)

// InputError is a user-caused error (CLI arguments, input files,
// front matter). It maps to exit code 2.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

// RuntimeError is an environment or renderer failure. It maps to exit code 1.
type RuntimeError struct{ msg string }

func (e *RuntimeError) Error() string { return e.msg }

// Input returns a formatted InputError.
func Input(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// Runtime returns a formatted RuntimeError.
func Runtime(format string, args ...any) error {
	return &RuntimeError{msg: fmt.Sprintf(format, args...)}
}

// ExitCode maps an error to a process exit code.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ie *InputError
	if errors.As(err, &ie) {
		return 2
	}
	return 1
}
