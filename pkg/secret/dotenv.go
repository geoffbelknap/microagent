package secret

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// parseDotenv parses dotenv-style KEY=VALUE lines into a map. Blank lines and
// lines beginning with '#' are ignored, a leading "export " is stripped, and a
// single matching pair of surrounding quotes is removed from the value. It is
// the shared parser for the dotenv scheme and the --secrets-env-file loader.
func parseDotenv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("dotenv line %d is not KEY=VALUE: %q", line, text)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("dotenv line %d has an empty key", line)
		}
		out[key] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dotenv: %w", err)
	}
	return out, nil
}

// unquote removes a single matching pair of surrounding single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
