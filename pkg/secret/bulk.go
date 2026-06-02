package secret

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// LoadEnvFile reads a dotenv file and returns all KEY=VALUE pairs. These values
// are operator-owned plaintext; callers must surface the plaintext warning when
// using them. (Flag wiring lands in sub-project #2.)
func LoadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets env file: %w", err)
	}
	return parseDotenv(data)
}

// LoadJSONMap reads a JSON object of string values from r (e.g. --secrets-stdin).
// Non-string values and a JSON null are rejected. Values are operator-owned
// plaintext.
func LoadJSONMap(r io.Reader) (map[string]string, error) {
	var out map[string]string
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("parse secrets JSON: %w", err)
	}
	if out == nil {
		return nil, fmt.Errorf("secrets JSON must be an object of string values")
	}
	return out, nil
}
