package problem

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestErrorReadsInRussianWithItsCause(t *testing.T) {
	err := Wrap("dir_create", &fs.PathError{Op: "mkdir", Path: "/x", Err: fs.ErrPermission}, "/x")
	if got, want := err.Error(), "не удалось создать каталог /x: mkdir /x: permission denied"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Error("the cause must stay reachable through errors.Is")
	}
}

func TestIsMatchesByCode(t *testing.T) {
	sentinel := New("no_isolation")
	if !errors.Is(Wrap("no_isolation", New("no_isolation_uid", 1000)), sentinel) {
		t.Error("an error of the sentinel's code must match it")
	}
	if errors.Is(New("plugin_closed"), sentinel) {
		t.Error("another code must not match")
	}
}

func TestFromIsAChainEndingInRaw(t *testing.T) {
	err := Wrap("plugin_settings_invalid", Wrap("web_allow_list", New("web_entry_port", "a.com:8080")))
	got := From(err)
	want := &Problem{Code: "plugin_settings_invalid", Cause: &Problem{
		Code: "web_allow_list", Cause: &Problem{Code: "web_entry_port", Params: map[string]string{"entry": "a.com:8080"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("From = %+v", got)
	}

	foreign := Wrap("plugin_start", errors.New("exec: \"python3\": executable file not found in $PATH"), "python3")
	if p := From(foreign); p.Cause == nil || p.Cause.Raw == "" || p.Cause.Code != "" {
		t.Errorf("a foreign cause is raw: %+v", p)
	}
	if p := From(Wrap("handshake_failed", context.DeadlineExceeded)); p.Cause == nil || p.Cause.Code != "timed_out" {
		t.Errorf("a timeout is coded: %+v", p)
	}
	if From(nil) != nil {
		t.Error("no error, no problem")
	}
}

// Every line of the table can be rendered and can carry a cause: its verbs are
// all %s and as many as its params, and it holds no ": ".
func TestEveryCodeIsWellFormed(t *testing.T) {
	verb := regexp.MustCompile(`%[^%]`)
	for code, d := range codes {
		vs := verb.FindAllString(d.ru, -1)
		if len(vs) != len(d.params) {
			t.Errorf("%s: %d verbs, %d params", code, len(vs), len(d.params))
		}
		for _, v := range vs {
			if v != "%s" {
				t.Errorf("%s: verb %s, only %%s", code, v)
			}
		}
		if strings.Contains(d.ru, ": ") {
			t.Errorf("%s: the line holds \": \", where a cause would start", code)
		}
		args := make([]any, len(d.params))
		for i := range args {
			args[i] = "x"
		}
		if s := New(code, args...).Error(); strings.Contains(s, "%!") {
			t.Errorf("%s renders as %q", code, s)
		}
		if !regexp.MustCompile(`^[a-z][a-z_]*$`).MatchString(code) {
			t.Errorf("code %q is not snake_case", code)
		}
	}
}
