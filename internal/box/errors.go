package box

import (
	"errors"
	"fmt"
)

type Error struct {
	Code    string
	Message string
	Context map[string]string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func NewError(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return "internal"
}

func NotFound(name string) error {
	return &Error{Code: "not_found", Message: fmt.Sprintf("box %q was not found", name), Context: map[string]string{"box": name}}
}

func IsNotFound(err error) bool { return ErrorCode(err) == "not_found" }
