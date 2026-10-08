package cli

import "errors"

// ExitError carries a process exit code. Every other error exits 1; adopt uses
// 2 (mismatch), 3 (ambiguous) and 4 (refused).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the process exit code for an error returned by Execute.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code > 0 {
		return ee.Code
	}
	return 1
}
