// Package mederr holds the three failures a MedGit record can raise.
package mederr

import "fmt"

// Error is a generic MedGit failure that is neither a policy nor an integrity fault.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Policy is a role or workflow rejection.
type Policy struct{ Msg string }

func (e *Policy) Error() string { return e.Msg }

// Integrity means the object store or the hash chain is invalid.
type Integrity struct{ Msg string }

func (e *Integrity) Error() string { return e.Msg }

// Rewrite is raised when a caller asks to amend, rebase, or reset history.
type Rewrite struct{ Msg string }

func (e *Rewrite) Error() string { return e.Msg }

// Errorf builds a generic MedGit error.
func Errorf(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// Policyf builds a policy rejection.
func Policyf(format string, args ...any) error {
	return &Policy{Msg: fmt.Sprintf(format, args...)}
}

// Integrityf builds an integrity failure.
func Integrityf(format string, args ...any) error {
	return &Integrity{Msg: fmt.Sprintf(format, args...)}
}
