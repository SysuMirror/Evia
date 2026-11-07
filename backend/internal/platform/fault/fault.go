// Package fault defines safe application errors independent of transport.
package fault

import "errors"

type Kind uint8

const (
	Invalid Kind = iota
	Unauthenticated
	Forbidden
	NotFound
	Conflict
	Unavailable
	Limited
	Internal
)

type Error struct {
	Kind    Kind
	Code    string
	Message string
	Fields  []string
	Cause   error
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func Public(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return &Error{Kind: Internal, Code: "INTERNAL_ERROR", Message: "请求未能完成", Cause: err}
}
