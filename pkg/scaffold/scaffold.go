// Package scaffold generates a starter agent project: a microagent.yaml
// spec, a provider-specific agent, the shared agent protocol, and a demo request.
// It is the library behind `microagent init` — the on-ramp that turns the
// minimal-agent example into a one-command starting point.
//
// scaffold owns file generation only. It does not build, create, or run the
// workspace; the generated project is consumed by the normal create/cp/start
// flow like any hand-written spec.
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

//go:embed templates
var templates embed.FS

// Provider selects the model-provider variant of the generated agent.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
)

// DefaultProvider is used when no provider is requested.
const DefaultProvider = ProviderAnthropic

type providerInfo struct {
	Label     string // human-facing name, e.g. "Anthropic"
	SDKSpec   string // pip requirement specifier installed by the spec's setup step
	APIKeyEnv string // environment variable the agent reads its key from
}

var providers = map[Provider]providerInfo{
	ProviderAnthropic: {Label: "Anthropic", SDKSpec: "anthropic>=0.40", APIKeyEnv: "ANTHROPIC_API_KEY"},
	ProviderOpenAI:    {Label: "OpenAI", SDKSpec: "openai>=1.50", APIKeyEnv: "OPENAI_API_KEY"},
	ProviderGemini:    {Label: "Gemini", SDKSpec: "google-genai>=0.3", APIKeyEnv: "GEMINI_API_KEY"},
}

// Providers returns the supported provider keys in stable order.
func Providers() []Provider {
	return []Provider{ProviderAnthropic, ProviderOpenAI, ProviderGemini}
}

// Options configures a scaffold generation.
type Options struct {
	// Name is the agent/workspace name written into the spec. Required.
	Name string
	// Dir is the target directory. Defaults to ./<Name> when empty.
	Dir string
	// Provider selects the agent variant. Defaults to DefaultProvider when empty.
	Provider Provider
	// Force overwrites existing files instead of failing.
	Force bool
}

// Result reports what a scaffold generation produced.
type Result struct {
	Name     string   `json:"name"`
	Provider Provider `json:"provider"`
	Dir      string   `json:"dir"`
	APIKey   string   `json:"api_key_env"`
	Files    []string `json:"files"` // paths relative to Dir, sorted
}

type fileSpec struct {
	out      string // output path relative to the target directory
	src      string // path within the embedded templates FS
	template bool   // render through text/template when true, copy verbatim otherwise
}

var fileSpecs = []fileSpec{
	{out: "microagent.yaml", src: "templates/microagent.yaml.tmpl", template: true},
	{out: "agent.py", src: "templates/agent/%s.py.tmpl", template: true},
	{out: "protocol.py", src: "templates/protocol.py"},
	{out: "README.md", src: "templates/README.md.tmpl", template: true},
	{out: "demo/README.md", src: "templates/demo/README.md"},
	{out: "demo/constraints.json", src: "templates/demo/constraints.json"},
	{out: "demo/system_prompt.md", src: "templates/demo/system_prompt.md"},
	{out: "demo/hello.json", src: "templates/demo/hello.json"},
	{out: "demo/input-001.json", src: "templates/demo/input-001.json"},
	{out: "demo/input-002.json", src: "templates/demo/input-002.json"},
	{out: "demo/clone-and-test.json", src: "templates/demo/clone-and-test.json"},
	{out: "demo/analyze-file.json", src: "templates/demo/analyze-file.json"},
	{out: "demo/data/sales-sample.csv", src: "templates/demo/data/sales-sample.csv"},
}

type templateData struct {
	Name      string
	AgentID   string
	Provider  string
	SDKSpec   string
	APIKeyEnv string
}

// Generate writes a scaffolded agent project and returns what it produced.
// It fails closed: if any target file already exists and Force is false, it
// writes nothing.
func Generate(opts Options) (Result, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return Result{}, fmt.Errorf("name is required")
	}
	if !vmkit.SafeIdentifier(name) {
		return Result{}, fmt.Errorf("invalid name %q: must not contain %q, %q, or NUL, and cannot be \".\" or \"..\"", name, "/", `\`)
	}

	provider := opts.Provider
	if provider == "" {
		provider = DefaultProvider
	}
	info, ok := providers[provider]
	if !ok {
		return Result{}, fmt.Errorf("unsupported provider %q: choose one of %s", provider, providerList())
	}

	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = name
	}

	data := templateData{
		Name:      name,
		AgentID:   name + "-1",
		Provider:  info.Label,
		SDKSpec:   info.SDKSpec,
		APIKeyEnv: info.APIKeyEnv,
	}

	// Resolve every source and target up front so we can fail closed before
	// touching the filesystem.
	type plannedFile struct {
		rel     string
		abs     string
		content []byte
	}
	planned := make([]plannedFile, 0, len(fileSpecs))
	for _, spec := range fileSpecs {
		src := spec.src
		if strings.Contains(src, "%s") {
			src = fmt.Sprintf(src, provider)
		}
		raw, err := templates.ReadFile(src)
		if err != nil {
			return Result{}, fmt.Errorf("read template %s: %w", src, err)
		}
		content := raw
		if spec.template {
			rendered, err := render(spec.out, raw, data)
			if err != nil {
				return Result{}, err
			}
			content = rendered
		}
		planned = append(planned, plannedFile{
			rel:     spec.out,
			abs:     filepath.Join(dir, filepath.FromSlash(spec.out)),
			content: content,
		})
	}

	if !opts.Force {
		var existing []string
		for _, f := range planned {
			if _, err := os.Stat(f.abs); err == nil {
				existing = append(existing, f.rel)
			} else if !os.IsNotExist(err) {
				return Result{}, fmt.Errorf("stat %s: %w", f.abs, err)
			}
		}
		if len(existing) > 0 {
			sort.Strings(existing)
			return Result{}, fmt.Errorf("refusing to overwrite existing file(s) in %s: %s (use --force to overwrite)", dir, strings.Join(existing, ", "))
		}
	}

	for _, f := range planned {
		if err := os.MkdirAll(filepath.Dir(f.abs), 0o755); err != nil {
			return Result{}, fmt.Errorf("create directory for %s: %w", f.rel, err)
		}
		if err := os.WriteFile(f.abs, f.content, 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", f.rel, err)
		}
	}

	files := make([]string, 0, len(planned))
	for _, f := range planned {
		files = append(files, f.rel)
	}
	sort.Strings(files)

	return Result{
		Name:     name,
		Provider: provider,
		Dir:      dir,
		APIKey:   info.APIKeyEnv,
		Files:    files,
	}, nil
}

func render(name string, raw []byte, data templateData) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render template %s: %w", name, err)
	}
	return []byte(buf.String()), nil
}

func providerList() string {
	keys := Providers()
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

// ensure embed.FS import is retained even if templates layout changes.
var _ fs.FS = templates
