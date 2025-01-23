package netplan

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	discardUnknownObjectRegex, _ = regexp.Compile(`^Unknown object '(.+)'$`)
)

type Error interface {
	ShouldRetry() bool
	Error() string
}
type UnknownError struct {
	Err error
}

func (e UnknownError) Error() string {
	return e.Err.Error()
}
func (e UnknownError) ShouldRetry() bool {
	return true
}

type MultipleErrors struct {
	Errors []Error
}

func (e MultipleErrors) Error() string {
	return fmt.Sprintf("the following errors were encountered: %v", e.Errors)
}
func (e MultipleErrors) ShouldRetry() bool {
	for _, err := range e.Errors {
		if !err.ShouldRetry() {
			return false
		}
	}
	return true
}

type OvsTimeoutError struct {
	Err error
}

func (e OvsTimeoutError) Error() string {
	return fmt.Sprintf("ovs-vsctl timeout error: %v", e.Err)
}
func (e OvsTimeoutError) ShouldRetry() bool {
	return true
}

type TransactionError struct {
	CommitError   Error
	RollbackError Error
}

func (e TransactionError) Error() string {
	if e.RollbackError != nil {
		return fmt.Sprintf("CRITICAL: MANUAL ACTION NEEDED -> the following error was received during Commit of configuration: %v - The following error was encountered trying to rollback: %v.", e.CommitError, e.RollbackError)
	}
	return fmt.Sprintf("the following error was received during Commit of configuration: %v - The configuration was rolled back successfully.", e.CommitError)
}
func (e TransactionError) ShouldRetry() bool {
	// If the rollback error is nil, then the rollback was successful so we will retry if the commit error is retryable
	return e.RollbackError == nil && e.CommitError.ShouldRetry()
}

type UnkownObjectError struct {
	Path string
}

func (e UnkownObjectError) Error() string {
	return fmt.Sprintf("unknown object with path %s", e.Path)
}
func (e UnkownObjectError) ShouldRetry() bool {
	return true
}

type InvalidConfigurationError struct {
	Err error
}

func (e InvalidConfigurationError) Error() string {
	return fmt.Sprintf("invalid netplan configuration. err: %s", e.Err)
}
func (e InvalidConfigurationError) ShouldRetry() bool {
	return false
}

type ConfigurationInvalidatedError struct{}

func (e ConfigurationInvalidatedError) ShouldRetry() bool {
	return true
}

func ParseError(err error) Error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "This config was invalidated by another config"):
		return ConfigurationInvalidatedError{}
	case strings.Contains(msg, "the configuration could not be generated"):
		return InvalidConfigurationError{Err: err}
	case strings.Contains(msg, "Error in network definition"):
		return InvalidConfigurationError{Err: err}
	case strings.Contains(msg, "matched more than one interface for a PF device"):
		return InvalidConfigurationError{Err: err}
	case strings.Contains(msg, "matched more than one interface for a VF device"):
		return InvalidConfigurationError{Err: err}
	case strings.Contains(msg, "Unknown object '"):
		matches := discardUnknownObjectRegex.FindStringSubmatch(err.Error())
		result := UnkownObjectError{}
		if len(matches) > 1 {
			result.Path = matches[1]
		}
		return result
	case regexp.MustCompile("Job for netplan-ovs-.+.service failed because a timeout was exceeded").Match([]byte(msg)):
		return OvsTimeoutError{Err: err}
	}

	return UnknownError{Err: err}
}
func (e ConfigurationInvalidatedError) Error() string {
	return "netplan configuration was invalidated"
}
func IsUnknownError(err Error) bool {
	_, ok := err.(UnknownError)
	return ok
}
