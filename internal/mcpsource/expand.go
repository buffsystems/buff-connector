package mcpsource

import (
	"regexp"
	"strings"

	"github.com/buffsystems/buff-connector/internal/problem"
)

var placeholder = regexp.MustCompile(`\$\{([^}]+)\}`)

// Expand fills ${name} placeholders from vars. A placeholder nobody can fill is
// an error naming it: an empty string handed to a server as its API key is a
// failure discovered much later and much further away.
func Expand(s string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-1]
		if v, ok := vars[key]; ok {
			return v
		}
		missing = append(missing, key)
		return m
	})
	if len(missing) > 0 {
		return "", problem.New("placeholder_missing", strings.Join(missing, ", "))
	}
	return out, nil
}
