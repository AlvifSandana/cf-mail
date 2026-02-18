package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrValidation indicates invalid domain/usecase input.
	ErrValidation = errors.New("domain validation error")
	// ErrNotFound indicates requested entity does not exist.
	ErrNotFound = errors.New("domain not found")
	// ErrDependency indicates an external dependency failure.
	ErrDependency = errors.New("domain dependency error")

	// Parser-related canonical domain errors.
	ErrNoRuleMatched = errors.New("no parser rule matched")
	ErrNoOTPFound    = errors.New("no otp found for matched rule")
	ErrAliasRequired = errors.New("incoming email alias recipient is required")
)

func WrapValidation(msg string, err error) error {
	return wrap(ErrValidation, msg, err)
}

func WrapNotFound(msg string, err error) error {
	return wrap(ErrNotFound, msg, err)
}

func WrapDependency(msg string, err error) error {
	return wrap(ErrDependency, msg, err)
}

func wrap(base error, msg string, err error) error {
	if err == nil {
		if msg == "" {
			return base
		}
		return fmt.Errorf("%w: %s", base, msg)
	}
	if msg == "" {
		return errors.Join(base, err)
	}
	return errors.Join(base, fmt.Errorf("%s: %w", msg, err))
}
