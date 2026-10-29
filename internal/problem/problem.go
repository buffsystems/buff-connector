// Package problem is how the connector says what went wrong without saying it
// in a language: a stable code, the values that fill it, and the cause it
// wraps. The connector reports it to the platform as data, and each client —
// the cabinet, the phone — writes the text itself, in its own reader's
// language, from its own catalogue (#565, #718).
//
// The connector reads its own problems too (the terminal, the local page, the
// log), so a coded error also has a text: the Russian line of its code, with
// the cause after ": ", translated on the way out like every other line.
package problem

import (
	"context"
	"fmt"
)

// Problem is the reported shape: either a coded link — code, params, and the
// cause it wraps — or, at the end of a chain, Raw: a text the connector did
// not write (an OS or network error, what a plugin printed), passed on as it is.
type Problem struct {
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	Cause  *Problem          `json:"cause,omitempty"`
	Raw    string            `json:"raw,omitempty"`
}

// Error is an error the connector constructs: one code from the table, its
// values in the table's order, and the error it wraps, if any.
type Error struct {
	Code   string
	Values []string
	cause  error
}

// New is a coded error with no cause. The values fill the code's params in
// order; anything is printed with %v.
func New(code string, values ...any) *Error {
	return Wrap(code, nil, values...)
}

// Wrap is a coded error around a cause.
func Wrap(code string, cause error, values ...any) *Error {
	vals := make([]string, len(values))
	for i, v := range values {
		vals[i] = fmt.Sprint(v)
	}
	return &Error{Code: code, Values: vals, cause: cause}
}

// Error is the Russian line, the cause after ": ".
func (e *Error) Error() string {
	d, ok := codes[e.Code]
	head := e.Code
	if ok {
		args := make([]any, len(e.Values))
		for i, v := range e.Values {
			args[i] = v
		}
		head = fmt.Sprintf(d.ru, args...)
	}
	if e.cause != nil {
		return head + ": " + e.cause.Error()
	}
	return head
}

func (e *Error) Unwrap() error { return e.cause }

// Is matches by code, so a sentinel made with New stands for every error of
// its code, whatever it wraps.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Params names the values by the code's table entry.
func (e *Error) Params() map[string]string {
	d := codes[e.Code]
	if len(d.params) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.params))
	for i, name := range d.params {
		if i < len(e.Values) {
			out[name] = e.Values[i]
		}
	}
	return out
}

// From turns an error into its report: the coded links as codes, the first
// error the connector did not write as Raw. A timeout and a cancellation are
// Go's own words for something the connector knows the meaning of, so they
// are coded too.
func From(err error) *Problem {
	switch {
	case err == nil:
		return nil
	case err == context.DeadlineExceeded:
		return &Problem{Code: "timed_out"}
	case err == context.Canceled:
		return &Problem{Code: "cancelled"}
	}
	if e, ok := err.(*Error); ok {
		return &Problem{Code: e.Code, Params: e.Params(), Cause: From(e.cause)}
	}
	return &Problem{Raw: err.Error()}
}
