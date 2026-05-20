package command

import (
	"errors"
	"strings"
	"syscall"
)

// IsQuietStartupBindError reports errors from a second instance or crash-loop restart
// (port/file descriptor temporarily unavailable). These should not spam stderr.
func IsQuietStartupBindError(err error) bool {
	for err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EAGAIN) {
			return true
		}
		msg := err.Error()
		if strings.Contains(msg, "address already in use") ||
			strings.Contains(msg, "resource temporarily unavailable") ||
			strings.Contains(msg, "too many open files") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
