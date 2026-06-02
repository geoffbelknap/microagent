package secretxfer

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidName reports whether name is a safe secret/file name: a leading letter or
// underscore followed by letters, digits, or underscores (the shape of an
// environment-variable name). This prevents path traversal out of the secrets
// directory and keeps names portable as filenames.
func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// WriteSecrets materializes bundle into root as one file per secret. root is
// created 0700; each file is written 0400 with the value verbatim (no added
// newline). Names are validated and must be unique. The caller is responsible
// for mounting root as a tmpfs.
func WriteSecrets(root string, bundle Bundle) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("chmod secrets dir: %w", err)
	}
	seen := make(map[string]struct{}, len(bundle.Secrets))
	for _, entry := range bundle.Secrets {
		if !ValidName(entry.Name) {
			return fmt.Errorf("invalid secret name %q", entry.Name)
		}
		if _, dup := seen[entry.Name]; dup {
			return fmt.Errorf("duplicate secret name %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
		path := filepath.Join(root, entry.Name)
		if err := os.WriteFile(path, entry.Value, 0o400); err != nil {
			return fmt.Errorf("write secret %q: %w", entry.Name, err)
		}
		if err := os.Chmod(path, 0o400); err != nil {
			return fmt.Errorf("chmod secret %q: %w", entry.Name, err)
		}
	}
	return nil
}
