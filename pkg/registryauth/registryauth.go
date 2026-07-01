// Package registryauth resolves OCI registry credentials without any dependency
// on Docker or Docker Desktop. It reads only static credential files in the
// standard `{"auths":{...}}` JSON shape and never invokes an external
// docker-credential-* helper, so a public pull always works anonymously and a
// configured-but-missing helper can never break a pull. Docker's own config
// (~/.docker/config.json) is never read.
//
// Resolution order (first match wins, all Docker-free):
//
//  1. $REGISTRY_AUTH_FILE — the vendor-neutral convention shared by Podman,
//     Skopeo, and Buildah.
//  2. ~/.microagent/auth.json — microagent's own credential file, written by
//     `microagent registry login`.
//  3. anonymous — no credentials (public images).
package registryauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// AuthFilePath is microagent's own registry credential file. It lives alongside
// the rest of microagent's home (~/.microagent) for consistency with kernels
// and workspace state. Returns "" only when the home directory is unknown.
func AuthFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".microagent", "auth.json")
}

// ConfigPaths returns the Docker-free credential-file search order. Every path
// is optional; callers skip the ones that do not exist. None of these files is
// ever consulted for credential helpers — only their inline "auths".
func ConfigPaths() []string {
	var paths []string
	if p := strings.TrimSpace(os.Getenv("REGISTRY_AUTH_FILE")); p != "" {
		paths = append(paths, p)
	}
	if p := AuthFilePath(); p != "" {
		paths = append(paths, p)
	}
	return paths
}

// Credential returns an ORAS credential function for host. It resolves against
// every existing file in ConfigPaths (a static-auth file store, never a helper)
// and falls back to anonymous when no file holds a credential for the host.
func Credential(host string) auth.CredentialFunc {
	var stores []credentials.Store
	for _, p := range ConfigPaths() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if store, err := credentials.NewFileStore(p); err == nil {
			stores = append(stores, store)
		}
	}
	switch len(stores) {
	case 0:
		return auth.StaticCredential(host, auth.Credential{})
	case 1:
		return credentials.Credential(stores[0])
	default:
		return credentials.Credential(credentials.NewStoreWithFallbacks(stores[0], stores[1:]...))
	}
}

// authFile is the on-disk shape of microagent's auth.json (a subset of the
// standard Docker/OCI config: the inline "auths" map only).
type authFile struct {
	Auths map[string]authEntry `json:"auths"`
}

type authEntry struct {
	// Auth is base64(username:password), the standard encoding.
	Auth string `json:"auth,omitempty"`
}

// Login stores a base64-encoded username:password for registry in
// microagent's auth file (0600), creating or updating the entry. The file is
// written atomically. Helpers are never involved.
func Login(registry, username, password string) error {
	registry = normalizeRegistry(registry)
	if registry == "" {
		return fmt.Errorf("registry is required")
	}
	if username == "" {
		return fmt.Errorf("username is required")
	}
	path := AuthFilePath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory for auth file")
	}
	file, err := load(path)
	if err != nil {
		return err
	}
	if file.Auths == nil {
		file.Auths = map[string]authEntry{}
	}
	file.Auths[registry] = authEntry{
		Auth: base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
	}
	return save(path, file)
}

// Logout removes registry's entry from microagent's auth file. It is not an
// error to log out of a registry that has no stored credential.
func Logout(registry string) error {
	registry = normalizeRegistry(registry)
	if registry == "" {
		return fmt.Errorf("registry is required")
	}
	path := AuthFilePath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory for auth file")
	}
	file, err := load(path)
	if err != nil {
		return err
	}
	if _, ok := file.Auths[registry]; !ok {
		return nil
	}
	delete(file.Auths, registry)
	return save(path, file)
}

// List returns the registries with a stored credential in microagent's auth
// file, sorted. It never returns secret material.
func List() ([]string, error) {
	path := AuthFilePath()
	if path == "" {
		return nil, fmt.Errorf("cannot determine home directory for auth file")
	}
	file, err := load(path)
	if err != nil {
		return nil, err
	}
	registries := make([]string, 0, len(file.Auths))
	for r := range file.Auths {
		registries = append(registries, r)
	}
	sort.Strings(registries)
	return registries, nil
}

// normalizeRegistry trims a registry reference to its host[:port], dropping any
// scheme or trailing path so `https://ghcr.io/` and `ghcr.io` resolve alike.
func normalizeRegistry(registry string) string {
	registry = strings.TrimSpace(registry)
	registry = strings.TrimPrefix(registry, "https://")
	registry = strings.TrimPrefix(registry, "http://")
	if i := strings.IndexByte(registry, '/'); i >= 0 {
		registry = registry[:i]
	}
	return registry
}

// load reads the auth file, returning an empty file when it does not exist.
func load(path string) (authFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return authFile{Auths: map[string]authEntry{}}, nil
		}
		return authFile{}, fmt.Errorf("read auth file %s: %w", path, err)
	}
	var file authFile
	if len(strings.TrimSpace(string(data))) == 0 {
		return authFile{Auths: map[string]authEntry{}}, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return authFile{}, fmt.Errorf("parse auth file %s: %w", path, err)
	}
	if file.Auths == nil {
		file.Auths = map[string]authEntry{}
	}
	return file, nil
}

// save writes the auth file atomically with 0600 permissions.
func save(path string, file authFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return fmt.Errorf("create temp auth file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write auth file %s: %w", path, err)
	}
	return nil
}
