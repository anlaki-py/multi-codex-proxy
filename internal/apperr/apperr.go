package apperr

import "fmt"

// Code groups failures so UI and API can show a stable hint.
type Code string

const (
	CodeConfig   Code = "config"
	CodeAuth     Code = "auth"
	CodeAccounts Code = "accounts"
	CodeUpstream Code = "upstream"
	CodeInput    Code = "input"
)

// Error carries op context plus a user-facing hint. Never holds tokens.
type Error struct {
	Op   string
	Code Code
	Hint string
	Err  error
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s. Hint: %s", e.Op, e.Err, e.Hint)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func New(op string, code Code, err error, hint string) *Error {
	return &Error{Op: op, Code: code, Err: err, Hint: hint}
}

// UserMessage is the one line the TUI and proxy show.
func UserMessage(err error) (string, string) {
	if ae, ok := err.(*Error); ok {
		return ae.Err.Error(), ae.Hint
	}
	if err == nil {
		return "", ""
	}
	return err.Error(), ""
}
