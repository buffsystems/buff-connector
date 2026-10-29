package i18n

import (
	"context"
	"net/http"
)

// The language of one CALL — a tool listing or a tool call the platform or a
// developer's Buff Code makes — is the caller's when it names one, so a model
// working in English is told about the tools and answered in English even by a
// connector set up in Russian. A caller that names none (every one built before
// it could) gets the connector's own language, as it always did.

// LangHeader names the caller's language on a request to the developer gateway.
const LangHeader = "X-Buff-Lang"

// ForCall is the language of a call whose caller named `given` (or nothing).
func ForCall(given string) Lang {
	if l, ok := Parse(given); ok {
		return l
	}
	return Default()
}

// ForCaller is ForCall for a request to the developer gateway.
func ForCaller(h http.Header) Lang { return ForCall(h.Get(LangHeader)) }

type langKey struct{}

// WithLang carries a call's language down to where its texts are made.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, langKey{}, l)
}

// Named is the language the call named, if it did.
func Named(ctx context.Context) (Lang, bool) {
	l, ok := ctx.Value(langKey{}).(Lang)
	return l, ok
}

// FromContext is the call's language: the one it carries, else the connector's own.
func FromContext(ctx context.Context) Lang {
	if l, ok := Named(ctx); ok {
		return l
	}
	return Default()
}
