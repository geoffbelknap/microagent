package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
)

func TestWriteInitInjectsCommandAndValidEnv(t *testing.T) {
	dir := t.TempDir()
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello world"}, "", map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, "", 0, 0, 0, nil, nil, "", "research")
	if err != nil {
		t.Fatalf("writeInit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sbin", "microagent-init"))
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "export GOOD_ENV='ok'") {
		t.Fatalf("init missing valid env: %s", text)
	}
	if !strings.Contains(text, "hostname 'research'") {
		t.Fatalf("init missing hostname setup: %s", text)
	}
	if strings.Contains(text, "bad-env") {
		t.Fatalf("init included invalid env: %s", text)
	}
	if !strings.Contains(text, "set -- '/bin/echo' 'hello world'") {
		t.Fatalf("init missing command: %s", text)
	}
	if !strings.Contains(text, "mkdir -p /proc /sys /dev") {
		t.Fatalf("init missing mount point setup: %s", text)
	}
}

func TestWriteInitCopiesGuestBinaryAndConfig(t *testing.T) {
	dir := t.TempDir()
	initBinary := filepath.Join(dir, "guestinit")
	if err := os.WriteFile(initBinary, []byte("guest-init"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo", "hello"}, "service", map[string]string{
		"GOOD_ENV": "ok",
		"bad-env":  "ignored",
	}, initBinary, 1024, 22222, 23222, []Mount{{Device: "/dev/vdb", Mountpoint: "/config", Mode: "ro"}}, []PortForward{{Protocol: "tcp", HostPort: 8080, GuestPort: 80}}, "/bin/bash", "research")
	if err != nil {
		t.Fatalf("writeInit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sbin", "microagent-init"))
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	if string(data) != "guest-init" {
		t.Fatalf("init binary = %q", data)
	}
	config, err := os.ReadFile(filepath.Join(dir, "etc", "microagent", "run.json"))
	if err != nil {
		t.Fatalf("read run config: %v", err)
	}
	text := string(config)
	if !strings.Contains(text, `"port":1024`) ||
		!strings.Contains(text, `"shellPort":22222`) ||
		!strings.Contains(text, `"execPort":23222`) ||
		!strings.Contains(text, `"mode":"service"`) ||
		!strings.Contains(text, `"/bin/echo"`) ||
		!strings.Contains(text, `"GOOD_ENV=ok"`) ||
		!strings.Contains(text, `"/config"`) ||
		!strings.Contains(text, `"consoleShell":"/bin/bash"`) ||
		!strings.Contains(text, `"hostname":"research"`) ||
		!strings.Contains(text, `"hostPort":8080`) ||
		strings.Contains(text, "bad-env") {
		t.Fatalf("unexpected run config: %s", text)
	}
}

func TestBuildCommandCanSkipImageCommand(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Entrypoint = []string{"/entrypoint"}
	image.Config.Cmd = []string{"serve"}

	got := buildCommand(BuildRequest{NoImageCommand: true}, image)
	if len(got) != 0 {
		t.Fatalf("buildCommand = %#v, want no command", got)
	}
}

func TestBuildCommandUsesImageCommandByDefault(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Entrypoint = []string{"/entrypoint"}
	image.Config.Cmd = []string{"serve"}

	got := buildCommand(BuildRequest{}, image)
	want := []string{"/entrypoint", "serve"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("buildCommand = %#v, want %#v", got, want)
	}
}

func TestBuildGuestEnvIncludesImageEnvAndRequestOverrides(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Env = []string{
		"PATH=/usr/local/bin:/usr/bin",
		"IMAGE_ONLY=present",
		"OVERRIDE=image",
		"bad-env=ignored",
	}

	got := buildGuestEnv(map[string]string{
		"OVERRIDE": "request",
		"REQUEST":  "present",
		"bad-env":  "ignored",
	}, image)

	for key, want := range map[string]string{
		"PATH":       "/usr/local/bin:/usr/bin",
		"IMAGE_ONLY": "present",
		"OVERRIDE":   "request",
		"REQUEST":    "present",
	} {
		if got[key] != want {
			t.Fatalf("env[%s] = %q, want %q in %#v", key, got[key], want, got)
		}
	}
	if _, ok := got["bad-env"]; ok {
		t.Fatalf("env included invalid name: %#v", got)
	}
}

func TestAppendGuestConfigResetKeepsImageEnv(t *testing.T) {
	image := ocispec.Image{}
	image.Config.Env = []string{
		"PATH=/usr/local/bin:/usr/bin",
		"IMAGE_ONLY=present",
	}
	req := BuildRequest{
		Env:          map[string]string{"OPERATOR": "set"},
		ResultPort:   1024,
		ShellPort:    24279,
		ExecPort:     25279,
		ConsoleShell: "/bin/bash",
		Hostname:     "research-vm",
		Mounts:       []Mount{{Device: "vdb", Mountpoint: "/config", Mode: "ro"}},
		HostForwards: []PortForward{{Protocol: "tcp", HostPort: 8080, GuestPort: 80}},
		FinalCommand: []string{"/bin/sh", "-lc", "/app/entrypoint.sh"},
	}
	command := []string{"/bin/sh", "-lc", "set -eu\necho setup"}

	got, err := appendGuestConfigReset(command, req, image)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-lc" {
		t.Fatalf("command = %#v", got)
	}
	if !strings.HasPrefix(got[2], "set -eu\necho setup\nprintf '%s\\n' '") ||
		!strings.HasSuffix(got[2], "' > /etc/microagent/run.json") {
		t.Fatalf("script = %q", got[2])
	}
	for _, want := range []string{
		`"command":["/bin/sh","-lc","/app/entrypoint.sh"]`,
		`"PATH=/usr/local/bin:/usr/bin"`,
		`"IMAGE_ONLY=present"`,
		`"OPERATOR=set"`,
		`"port":1024`,
		`"shellPort":24279`,
		`"execPort":25279`,
		`"mountpoint":"/config"`,
		`"hostPort":8080`,
		`"consoleShell":"/bin/bash"`,
		`"hostname":"research-vm"`,
	} {
		if !strings.Contains(got[2], want) {
			t.Fatalf("script missing %q: %q", want, got[2])
		}
	}
	if strings.Contains(command[2], "run.json") {
		t.Fatalf("input command mutated: %q", command[2])
	}
}

func TestAppendGuestConfigResetAllowsEmptyFinalCommand(t *testing.T) {
	got, err := appendGuestConfigReset([]string{"/bin/sh", "-lc", "echo setup"}, BuildRequest{ResultPort: 1024}, ocispec.Image{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[2], `"command":[]`) {
		t.Fatalf("script = %q", got[2])
	}
}

func TestAppendGuestConfigResetRejectsNonShellCommand(t *testing.T) {
	if _, err := appendGuestConfigReset([]string{"/entrypoint"}, BuildRequest{}, ocispec.Image{}); err == nil {
		t.Fatal("expected error for non-shell command")
	}
}

func TestSplitRegistryReferenceNormalizesDefaultRegistryRefs(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantRepoRef   string
		wantReference string
	}{
		{
			name:          "official image with tag",
			raw:           "ubuntu:24.04",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "24.04",
		},
		{
			name:          "official image with digest",
			raw:           "ubuntu@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:          "default registry namespace with tag",
			raw:           "homebridge/homebridge:latest",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "default registry namespace without tag",
			raw:           "homebridge/homebridge",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "explicit default registry official shorthand",
			raw:           "docker.io/ubuntu:24.04",
			wantRepoRef:   "docker.io/library/ubuntu",
			wantReference: "24.04",
		},
		{
			name:          "explicit default registry namespace",
			raw:           "docker.io/homebridge/homebridge:latest",
			wantRepoRef:   "docker.io/homebridge/homebridge",
			wantReference: "latest",
		},
		{
			name:          "explicit ghcr registry",
			raw:           "ghcr.io/example/agent:latest",
			wantRepoRef:   "ghcr.io/example/agent",
			wantReference: "latest",
		},
		{
			name:          "explicit localhost registry",
			raw:           "localhost:5000/example/agent:latest",
			wantRepoRef:   "localhost:5000/example/agent",
			wantReference: "latest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRepoRef, gotReference, err := splitRegistryReference(tc.raw)
			if err != nil {
				t.Fatalf("splitRegistryReference: %v", err)
			}
			if gotRepoRef != tc.wantRepoRef || gotReference != tc.wantReference {
				t.Fatalf("splitRegistryReference(%q) = %q, %q; want %q, %q", tc.raw, gotRepoRef, gotReference, tc.wantRepoRef, tc.wantReference)
			}
		})
	}
}

// TestNewRepositoryUsesRegistryCredentialConfig verifies newRepository wires
// the Docker-free credential resolver (pkg/registryauth) into the ORAS client.
// Resolution semantics themselves are covered by registryauth's own tests.
func TestNewRepositoryUsesRegistryCredentialConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	authPath := filepath.Join(dir, "auth.json")
	encoded := base64.StdEncoding.EncodeToString([]byte("microagent-user:microagent-pass"))
	configJSON := `{"auths":{"example.com":{"auth":"` + encoded + `"}}}`
	if err := os.WriteFile(authPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGISTRY_AUTH_FILE", authPath)
	repo, err := newRepository("example.com/acme/image")
	if err != nil {
		t.Fatalf("newRepository: %v", err)
	}
	client, ok := repo.Client.(*auth.Client)
	if !ok {
		t.Fatalf("repo.Client = %T, want *auth.Client", repo.Client)
	}
	cred, err := client.Credential(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Username != "microagent-user" || cred.Password != "microagent-pass" {
		t.Fatalf("credential = %#v", cred)
	}
}

func TestNewRepositoryAllowsAnonymousPullWithoutCredentialConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", "")
	repo, err := newRepository("example.com/acme/image")
	if err != nil {
		t.Fatalf("newRepository: %v", err)
	}
	client, ok := repo.Client.(*auth.Client)
	if !ok {
		t.Fatalf("repo.Client = %T, want *auth.Client", repo.Client)
	}
	cred, err := client.Credential(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred != (auth.Credential{}) {
		t.Fatalf("credential = %#v, want empty", cred)
	}
}

func TestNewRepositoryAllowsAnonymousPullWithMissingCredentialHelper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	configJSON := `{"credHelpers":{"example.com":"microagent-missing-helper"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := newRepository("example.com/acme/image")
	if err != nil {
		t.Fatalf("newRepository: %v", err)
	}
	client, ok := repo.Client.(*auth.Client)
	if !ok {
		t.Fatalf("repo.Client = %T, want *auth.Client", repo.Client)
	}
	cred, err := client.Credential(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred != (auth.Credential{}) {
		t.Fatalf("credential = %#v, want empty", cred)
	}
}

func TestNewRepositoryUsesPlainHTTPOnlyForLoopbackRegistries(t *testing.T) {
	tests := []struct {
		repoRef string
		want    bool
	}{
		{repoRef: "localhost:5000/acme/image", want: true},
		{repoRef: "127.0.0.1:5000/acme/image", want: true},
		{repoRef: "[::1]:5000/acme/image", want: true},
		{repoRef: "registry.example.com/acme/image", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.repoRef, func(t *testing.T) {
			repo, err := newRepository(tc.repoRef)
			if err != nil {
				t.Fatalf("newRepository: %v", err)
			}
			if repo.PlainHTTP != tc.want {
				t.Fatalf("PlainHTTP = %v, want %v", repo.PlainHTTP, tc.want)
			}
		})
	}
}

func TestBuilderPullsFromPrivateRegistryUsingCredentialConfig(t *testing.T) {
	if os.Getenv("MICROAGENT_ROOTFS_REGISTRY_AUTH_E2E") != "1" {
		t.Skip("set MICROAGENT_ROOTFS_REGISTRY_AUTH_E2E=1 to run private registry rootfs E2E")
	}
	// The credential contract under test is format-agnostic; use the host's
	// native rootfs format so Windows (VHD, no mke2fs) can run the same E2E.
	format := FormatExt4
	output := "rootfs.ext4"
	mke2fsPath := ""
	if runtime.GOOS == "windows" {
		format = FormatVHD
		output = "rootfs.vhd"
	} else {
		path, err := exec.LookPath("mke2fs")
		if err != nil {
			t.Fatalf("mke2fs is required for private registry rootfs E2E: %v", err)
		}
		mke2fsPath = path
	}

	registry := newPrivateRegistryFixture(t)
	defer registry.close()
	writeRegistryCredentialConfig(t, registry.host, "microagent-user", "microagent-pass")

	dir := t.TempDir()
	provenance, err := NewBuilder().Build(context.Background(), BuildRequest{
		ImageRef:     registry.ref,
		Platform:     Platform{OS: "linux", Architecture: "amd64"},
		OutputPath:   filepath.Join(dir, output),
		Format:       format,
		StateDir:     filepath.Join(dir, "state"),
		Mke2fsPath:   mke2fsPath,
		SizeMiB:      64,
		AllowMutable: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, output)); err != nil {
		t.Fatalf("rootfs output: %v", err)
	}
	wantResolved := registry.host + "/microagent/private@" + registry.manifestDigest.String()
	if provenance.ResolvedRef != wantResolved {
		t.Fatalf("ResolvedRef = %q, want %q", provenance.ResolvedRef, wantResolved)
	}
	if !registry.sawAuthorizedPath("/v2/microagent/private/manifests/latest") ||
		!registry.sawAuthorizedPath("/v2/microagent/private/blobs/"+registry.configDigest.String()) ||
		!registry.sawAuthorizedPath("/v2/microagent/private/blobs/"+registry.layerDigest.String()) {
		t.Fatalf("registry did not see authorized manifest/config/layer fetches: %#v", registry.authorizedPaths())
	}
}

func TestBuilderRejectsPrivateRegistryWithoutCredentials(t *testing.T) {
	if os.Getenv("MICROAGENT_ROOTFS_REGISTRY_AUTH_E2E") != "1" {
		t.Skip("set MICROAGENT_ROOTFS_REGISTRY_AUTH_E2E=1 to run private registry rootfs E2E")
	}
	registry := newPrivateRegistryFixture(t)
	defer registry.close()
	// No credentials anywhere: isolate HOME (no ~/.microagent/auth.json) and
	// clear REGISTRY_AUTH_FILE so the pull is anonymous and must be rejected.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", "")

	dir := t.TempDir()
	_, err := NewBuilder().Build(context.Background(), BuildRequest{
		ImageRef:     registry.ref,
		Platform:     Platform{OS: "linux", Architecture: "amd64"},
		OutputPath:   filepath.Join(dir, "rootfs.ext4"),
		StateDir:     filepath.Join(dir, "state"),
		SizeMiB:      64,
		AllowMutable: true,
	})
	if err == nil {
		t.Fatal("Build succeeded without registry credentials")
	}
	if !strings.Contains(err.Error(), "fetch OCI image") {
		t.Fatalf("Build error = %v, want OCI fetch failure", err)
	}
	if registry.sawAuthorizedPath("/v2/microagent/private/manifests/latest") {
		t.Fatalf("registry saw authorized manifest fetch without credentials")
	}
}

type privateRegistryFixture struct {
	server         *httptest.Server
	host           string
	ref            string
	manifestDigest digest.Digest
	configDigest   digest.Digest
	layerDigest    digest.Digest

	mu         sync.Mutex
	authorized map[string]int
}

func newPrivateRegistryFixture(t *testing.T) *privateRegistryFixture {
	t.Helper()
	layerBytes := testTarLayer(t, "etc/microagent-auth-e2e.txt", "registry-auth-ok\n")
	layerDigest := digest.FromBytes(layerBytes)
	configBytes := mustJSON(t, map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{layerDigest.String()},
		},
		"config": map[string]any{
			"Env":        []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			"Entrypoint": []string{"/bin/sh"},
			"Cmd":        []string{"-c", "cat /etc/microagent-auth-e2e.txt"},
		},
	})
	configDigest := digest.FromBytes(configBytes)
	manifestBytes := mustJSON(t, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configBytes)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    layerDigest,
			Size:      int64(len(layerBytes)),
		}},
	})
	manifestDigest := digest.FromBytes(manifestBytes)

	fixture := &privateRegistryFixture{
		manifestDigest: manifestDigest,
		configDigest:   configDigest,
		layerDigest:    layerDigest,
		authorized:     map[string]int{},
	}
	blobs := map[string][]byte{
		configDigest.String(): configBytes,
		layerDigest.String():  layerBytes,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fixture.authorize(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/microagent/private/manifests/latest":
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(manifestBytes)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(manifestBytes)
			}
		case strings.HasPrefix(r.URL.Path, "/v2/microagent/private/blobs/"):
			dgst := strings.TrimPrefix(r.URL.Path, "/v2/microagent/private/blobs/")
			data, ok := blobs[dgst]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Docker-Content-Digest", dgst)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	fixture.host = strings.TrimPrefix(fixture.server.URL, "http://")
	fixture.ref = fixture.host + "/microagent/private:latest"
	return fixture
}

func (f *privateRegistryFixture) authorize(w http.ResponseWriter, r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if ok && user == "microagent-user" && pass == "microagent-pass" {
		f.mu.Lock()
		f.authorized[r.URL.Path]++
		f.mu.Unlock()
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="microagent private registry"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return false
}

func (f *privateRegistryFixture) sawAuthorizedPath(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorized[path] > 0
}

func (f *privateRegistryFixture) authorizedPaths() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := map[string]int{}
	for path, count := range f.authorized {
		paths[path] = count
	}
	return paths
}

func (f *privateRegistryFixture) close() {
	f.server.Close()
}

func writeRegistryCredentialConfig(t *testing.T, host, username, password string) {
	t.Helper()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("REGISTRY_AUTH_FILE", authPath)
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	configJSON := `{"auths":{"` + host + `":{"auth":"` + encoded + `"}}}`
	if err := os.WriteFile(authPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testTarLayer(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	header := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWriteDeclaredFilesCopiesSourceIntoStage(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sh")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "/app/source.sh", Mode: "0700"}}); err != nil {
		t.Fatalf("writeDeclaredFiles: %v", err)
	}
	target := filepath.Join(dir, "app", "source.sh")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho ok\n" {
		t.Fatalf("target content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		modes, err := readStageModes(dir)
		if err != nil {
			t.Fatalf("read stage modes after host mode %#o: %v", info.Mode().Perm(), err)
		}
		if modes["app/source.sh"] != 0o700 {
			t.Fatalf("host mode = %#o and recorded mode = %#o, want recorded 0700", info.Mode().Perm(), modes["app/source.sh"])
		}
	}
}

func TestWriteDeclaredFilesRejectsRelativeGuestPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "app/source.txt"}}); err == nil {
		t.Fatal("expected relative guest path to be rejected")
	}
}

func TestWriteInitDoesNotFollowStageSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "sbin")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	err := writeInit(dir, "/sbin/microagent-init", []string{"/bin/echo"}, "", nil, "", 0, 0, 0, nil, nil, "", "")
	if err == nil {
		t.Fatal("expected symlinked init parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "microagent-init")); !os.IsNotExist(err) {
		t.Fatalf("init escaped stage or stat failed: %v", err)
	}
}

func TestWriteDeclaredFilesDoesNotFollowStageSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "app")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	err := writeDeclaredFiles(dir, []File{{SourcePath: src, Path: "/app/source.txt"}})
	if err == nil {
		t.Fatal("expected symlinked file parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("declared file escaped stage or stat failed: %v", err)
	}
}

func TestExtractLayerAllowsAbsoluteSymlinkWithinGuestRoot(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "etc/alternatives/awk", Typeflag: tar.TypeSymlink, Linkname: "/usr/bin/mawk", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extractLayer: %v", err)
	}
	target, err := readExtractedSymlinkTarget(filepath.Join(dir, "etc", "alternatives", "awk"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/usr/bin/mawk" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestExtractLayerRejectsBackslashTraversalEntryName(t *testing.T) {
	// safeGuestRel reasons about slash-separated paths; backslashes are plain
	// name characters there but act as separators on Windows filesystem APIs,
	// so a name like `..\..\evil` must be rejected for every entry type.
	for _, header := range []*tar.Header{
		{Name: `..\..\evil`, Typeflag: tar.TypeSymlink, Linkname: "target", Mode: 0o777},
		{Name: `app\..\..\evil`, Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: `..\..\evil`, Typeflag: tar.TypeDir, Mode: 0o755},
	} {
		dir := t.TempDir()
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("close tar: %v", err)
		}
		if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
			t.Fatalf("expected backslash entry name %q to be rejected", header.Name)
		}
	}
}

func TestExtractLayerRejectsBackslashSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app/link", Typeflag: tar.TypeSymlink, Linkname: `..\..\outside`, Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected backslash symlink target to be rejected")
	}
}

func TestExtractLayerRejectsBackslashHardlinkTarget(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app/link", Typeflag: tar.TypeLink, Linkname: `..\..\outside`, Mode: 0o644}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected backslash hardlink target to be rejected")
	}
}

func TestExtractLayerRejectsAbsoluteSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app", Typeflag: tar.TypeSymlink, Linkname: "/../../outside", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected absolute symlink escape to be rejected")
	}
}

func TestExtractLayerAllowsRelativeSymlinkWithinGuestRoot(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "bin", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatalf("write dir: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "bin/env", Mode: 0o755, Size: 2}); err != nil {
		t.Fatalf("write file header: %v", err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "usr/bin/env", Typeflag: tar.TypeSymlink, Linkname: "../../bin/env", Mode: 0o777}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extractLayer: %v", err)
	}
	target, err := readExtractedSymlinkTarget(filepath.Join(dir, "usr", "bin", "env"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../../bin/env" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestWriteStageTarPreservesWindowsSymlinkMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSymlinkMarker(filepath.Join(dir, "usr", "bin", "env"), "../../bin/env"); err != nil {
		t.Fatalf("writeSymlinkMarker: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if _, err := writeStageTar(dir, tw); err != nil {
		t.Fatalf("writeStageTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		header, err := tr.Next()
		if err != nil {
			t.Fatalf("missing symlink entry: %v", err)
		}
		if header.Name != "usr/bin/env" {
			continue
		}
		if header.Typeflag != tar.TypeSymlink || header.Linkname != "../../bin/env" {
			t.Fatalf("header = %#v, want symlink to ../../bin/env", header)
		}
		return
	}
}

func TestWriteStageTarPreservesRecordedMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sbin", "microagent-init"), []byte("guest-init"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := recordStageMode(root, "sbin/microagent-init", 0o755); err != nil {
		t.Fatalf("recordStageMode: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if _, err := writeStageTar(dir, tw); err != nil {
		t.Fatalf("writeStageTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		header, err := tr.Next()
		if err != nil {
			t.Fatalf("missing init entry: %v", err)
		}
		if header.Name != "sbin/microagent-init" {
			continue
		}
		if os.FileMode(header.Mode).Perm() != 0o755 {
			t.Fatalf("mode = %#o, want 0755", os.FileMode(header.Mode).Perm())
		}
		return
	}
}

func TestEnsureGuestRuntimeDirsCreatesMountpointsAndRecordsModes(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGuestRuntimeDirs(dir); err != nil {
		t.Fatalf("ensureGuestRuntimeDirs: %v", err)
	}
	for _, rel := range []string{"proc", "sys", filepath.Join("dev", "pts")} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", rel)
		}
	}
	modes, err := readStageModes(dir)
	if err != nil {
		t.Fatalf("readStageModes: %v", err)
	}
	if modes["proc"] != 0o755 || modes["sys"] != 0o755 || modes["dev/pts"] != 0o755 {
		t.Fatalf("runtime dir modes = %#v", modes)
	}
}

func TestExtractLayerRejectsRelativeSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "app/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside", Mode: 0o777}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected relative symlink escape to be rejected")
	}
}

func TestExtractLayerRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 4}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("nope")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected traversal entry to be rejected")
	}
}

func TestExtractLayerAppliesWhiteout(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := addTarFile(tw, "etc/old", "old"); err != nil {
		t.Fatalf("add old: %v", err)
	}
	if err := addTarFile(tw, "etc/.wh.old", ""); err != nil {
		t.Fatalf("add whiteout: %v", err)
	}
	if err := addTarFile(tw, "etc/new", "new"); err != nil {
		t.Fatalf("add new: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := extractLayer(dir, "application/vnd.oci.image.layer.v1.tar", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("extract layer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "old")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "etc", "new")); err != nil || string(data) != "new" {
		t.Fatalf("new file = %q, %v", string(data), err)
	}
}

func addTarFile(tw *tar.Writer, name, body string) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
		return err
	}
	_, err := tw.Write([]byte(body))
	return err
}

func readExtractedSymlinkTarget(path string) (string, error) {
	target, err := os.Readlink(path)
	if err == nil {
		return target, nil
	}
	if markerTarget, ok, markerErr := readSymlinkMarker(path); markerErr == nil && ok {
		return markerTarget, nil
	}
	return "", err
}
