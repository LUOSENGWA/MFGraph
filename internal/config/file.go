package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxFileBytes bounds a configuration file.
const maxFileBytes = 1 << 20

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ReadFile reads the configuration file of --config (see ParseFile). A
// relative path is relative to the working directory.
func ReadFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: --config: %w", err)
	}
	return ParseFile(path, b)
}

// ParseFile parses a configuration file in env format:
//
//   - one KEY=VALUE per line; the first '=' separates the key from the value,
//     and white space around both is removed;
//   - blank lines and lines whose first non-blank character is '#' are
//     comments ('#' anywhere else is part of the value);
//   - nothing else is interpreted: no quotes, escapes or variable expansion.
//     A line that contains "${" is refused, so that what looks like an
//     expansion is never taken literally;
//   - KEY must be one of Variables, and may appear once.
//
// Errors name the file, the line and the key, never a value (values may be
// secrets).
func ParseFile(name string, b []byte) (map[string]string, error) {
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("config: %s: larger than %d bytes", name, maxFileBytes)
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("config: %s: not valid UTF-8", name)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(format string, args ...any) error {
			return fmt.Errorf("config: %s line %d: "+format, append([]any{name, i + 1}, args...)...)
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !keyRe.MatchString(key) {
			// The line is not shown: it may be a misplaced secret.
			return nil, bad("want KEY=VALUE with a variable name such as %s", EnvMode)
		}
		if strings.Contains(line, "${") {
			return nil, bad("%s: variable expansion (${...}) is not supported; write the value itself", key)
		}
		if !slices.Contains(Variables, key) {
			return nil, bad("unknown variable %s", truncateKey(key))
		}
		if _, dup := out[key]; dup {
			return nil, bad("%s is set twice", key)
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, nil
}

// truncateKey shortens an unknown key for an error message.
func truncateKey(k string) string {
	if len(k) > 64 {
		return k[:64] + "..."
	}
	return k
}
