package config

import (
	"fmt"
	"os"
	"strings"
)

// Secrets are never written in the config file. Each one names either an
// environment variable or a file that the agent reads at load time, so the
// secret itself stays out of the image, out of git, and out of our reach.

// exactlyOneSecretRef reports a config error unless exactly one of the two
// references is set.
func exactlyOneSecretRef(name, env, file string) error {
	if (env != "") == (file != "") {
		return fmt.Errorf("exactly one of %s_env or %s_file must be set", name, name)
	}
	return nil
}

// resolveSecret reads a secret from whichever reference is set, trimming
// surrounding whitespace so a trailing newline in a mounted file is harmless.
// With neither reference set it returns an empty string and no error: some
// secrets are optional.
func resolveSecret(name, env, file string) (string, error) {
	switch {
	case env != "":
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			return "", fmt.Errorf("%s_env %s is empty or unset", name, env)
		}
		return v, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s_file: %w", name, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("%s_file %s is empty", name, file)
		}
		return v, nil
	default:
		return "", nil
	}
}
