package registryauth

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"
)

// isolate points HOME and the relevant env at a clean temp dir so tests never
// touch the developer's real ~/.microagent or ~/.docker.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("DOCKER_CONFIG", "")
	return dir
}

func staticAuthJSON(host, user, pass string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return `{"auths":{"` + host + `":{"auth":"` + enc + `"}}}`
}

func TestCredentialAnonymousWhenNothingConfigured(t *testing.T) {
	isolate(t)
	cred, err := Credential("example.com")(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred != (auth.Credential{}) {
		t.Fatalf("credential = %#v, want anonymous", cred)
	}
}

func TestCredentialReadsRegistryAuthFile(t *testing.T) {
	dir := isolate(t)
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(staticAuthJSON("example.com", "envuser", "envpass")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGISTRY_AUTH_FILE", authPath)
	cred, err := Credential("example.com")(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Username != "envuser" || cred.Password != "envpass" {
		t.Fatalf("credential = %#v", cred)
	}
}

// TestCredentialNeverReadsDockerConfig is the core of the purge: a docker
// config (even with a static auth for the host) is never read, so a missing
// credential helper can never be invoked and resolution stays anonymous.
func TestCredentialNeverReadsDockerConfig(t *testing.T) {
	dir := isolate(t)
	dockerDir := filepath.Join(dir, ".docker")
	if err := os.MkdirAll(dockerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"auths":{"example.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("dockeruser:dockerpass")) + `"}},"credsStore":"this-helper-does-not-exist"}`
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dockerDir)
	cred, err := Credential("example.com")(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential resolved with error (docker config read?): %v", err)
	}
	if cred != (auth.Credential{}) {
		t.Fatalf("credential = %#v, want anonymous (docker config must never be read)", cred)
	}
}

func TestLoginListLogoutRoundTrip(t *testing.T) {
	isolate(t)
	if err := Login("ghcr.io", "octocat", "ghp_secret"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := Login("https://registry.example.com/v2/", "alice", "pw"); err != nil {
		t.Fatalf("Login (normalizing): %v", err)
	}

	// Stored credential is resolvable via the default search order.
	cred, err := Credential("ghcr.io")(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Username != "octocat" || cred.Password != "ghp_secret" {
		t.Fatalf("credential = %#v", cred)
	}

	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"ghcr.io", "registry.example.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("List = %v, want %v (scheme/path should be normalized away)", got, want)
	}

	// File must be 0600 — it holds secret material.
	info, err := os.Stat(AuthFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth file perm = %o, want 600", perm)
	}

	if err := Logout("ghcr.io"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	got, err = List()
	if err != nil {
		t.Fatalf("List after logout: %v", err)
	}
	if len(got) != 1 || got[0] != "registry.example.com" {
		t.Fatalf("List after logout = %v, want [registry.example.com]", got)
	}

	// Logging out of an unknown registry is a no-op, not an error.
	if err := Logout("nope.example.com"); err != nil {
		t.Fatalf("Logout unknown: %v", err)
	}
}

// TestLoginDockerHubResolvesForPullHost proves a Docker Hub credential logged in
// under any of its user-facing aliases resolves for the host ORAS actually looks
// it up by at pull time (registry-1.docker.io → https://index.docker.io/v1/).
// Storing it under the literal alias key ("docker.io") means the credential is
// never sent and a private Hub pull 401s.
func TestLoginDockerHubResolvesForPullHost(t *testing.T) {
	for _, alias := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
		t.Run(alias, func(t *testing.T) {
			isolate(t)
			if err := Login(alias, "hubuser", "hubpass"); err != nil {
				t.Fatalf("Login(%q): %v", alias, err)
			}
			cred, err := Credential("registry-1.docker.io")(context.Background(), "registry-1.docker.io")
			if err != nil {
				t.Fatalf("Credential: %v", err)
			}
			if cred.Username != "hubuser" || cred.Password != "hubpass" {
				t.Fatalf("Hub credential (login %q) did not resolve for the pull host: %#v", alias, cred)
			}
			if err := Logout(alias); err != nil {
				t.Fatalf("Logout(%q): %v", alias, err)
			}
			cred, _ = Credential("registry-1.docker.io")(context.Background(), "registry-1.docker.io")
			if cred.Username != "" || cred.Password != "" {
				t.Fatalf("Logout(%q) did not remove the Hub credential: %#v", alias, cred)
			}
		})
	}
}
