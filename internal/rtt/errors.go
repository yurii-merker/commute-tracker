package rtt

import "errors"

type TransientError struct {
	Cause error
}

func (e *TransientError) Error() string {
	return "transient rtt error: " + e.Cause.Error()
}

func (e *TransientError) Unwrap() error {
	return e.Cause
}

type PermanentError struct {
	Cause error
}

func (e *PermanentError) Error() string {
	return "permanent rtt error: " + e.Cause.Error()
}

func (e *PermanentError) Unwrap() error {
	return e.Cause
}

func IsTransient(err error) bool {
	var target *TransientError
	return errors.As(err, &target)
}
