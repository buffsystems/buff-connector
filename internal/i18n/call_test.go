package i18n

import (
	"context"
	"net/http"
	"testing"
)

// The language of one call: the caller's when it names one of ours, else the
// connector's own, which is Russian when nothing says otherwise.
func TestForCall(t *testing.T) {
	defer SetDefault(Default())
	SetDefault(RU)
	cases := []struct {
		given string
		want  Lang
	}{
		{"en", EN},
		{"en-GB", EN},
		{"ru", RU},
		{"", RU},     // an older caller names none: the connector's own
		{"de", RU},   // one we lack: the connector's own
		{" EN ", EN}, // tolerant of case and space
	}
	for _, c := range cases {
		if got := ForCall(c.given); got != c.want {
			t.Errorf("ForCall(%q) = %s, want %s", c.given, got, c.want)
		}
	}
	SetDefault(EN)
	if got := ForCall(""); got != EN {
		t.Errorf("no caller language on an English connector: got %s", got)
	}
	if got := ForCall("ru"); got != RU {
		t.Errorf("the caller's Russian beats the connector's English: got %s", got)
	}
}

func TestForCaller(t *testing.T) {
	defer SetDefault(Default())
	SetDefault(RU)
	h := http.Header{}
	if got := ForCaller(h); got != RU {
		t.Errorf("no header: got %s", got)
	}
	h.Set(LangHeader, "en")
	if got := ForCaller(h); got != EN {
		t.Errorf("X-Buff-Lang en: got %s", got)
	}
}

func TestLangInContext(t *testing.T) {
	defer SetDefault(Default())
	SetDefault(RU)
	if got := FromContext(context.Background()); got != RU {
		t.Errorf("nothing in the context is the connector's own: got %s", got)
	}
	if got := FromContext(WithLang(context.Background(), EN)); got != EN {
		t.Errorf("got %s", got)
	}
	if _, ok := Named(context.Background()); ok {
		t.Error("a context with no language names none")
	}
	if l, ok := Named(WithLang(context.Background(), EN)); !ok || l != EN {
		t.Errorf("Named: %s %v", l, ok)
	}
}
