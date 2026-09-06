package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

const pairTestRef = "hf.co/test/model@main/model.gguf"

type pairingProbe struct {
	steps   []string
	pull    model.PullOptions
	runner  modelrunner.EnsureOptions
	service Options
	failAt  string
	cached  bool
}

func stubPairing(t *testing.T) *pairingProbe {
	t.Helper()
	oldFind, oldPull, oldEnsure, oldAttach, oldRunnerRelease, oldServiceRelease := findModel, pullModel, ensureModelRunner, attachModelService, releaseModelRunner, releaseModelService
	t.Cleanup(func() {
		findModel, pullModel, ensureModelRunner, attachModelService, releaseModelRunner, releaseModelService = oldFind, oldPull, oldEnsure, oldAttach, oldRunnerRelease, oldServiceRelease
	})
	for _, key := range []string{envModelMediation, envModelPolicyURL, envModelPolicyFile, envModelPolicyTimeout, "HF_TOKEN", "HUGGING_FACE_HUB_TOKEN"} {
		t.Setenv(key, "")
	}
	p := &pairingProbe{}
	findModel = func(_, ref string) (model.Record, error) {
		p.steps = append(p.steps, "find")
		if ref != pairTestRef {
			t.Fatalf("canonical lookup = %q", ref)
		}
		if p.cached {
			return model.Record{ModelRef: ref, OutputPath: "/cached.gguf"}, nil
		}
		return model.Record{}, os.ErrNotExist
	}
	pullModel = func(_ context.Context, opts model.PullOptions) (model.Record, error) {
		p.steps, p.pull = append(p.steps, "pull"), opts
		if p.failAt == "pull" {
			return model.Record{}, errors.New("pull failed")
		}
		return model.Record{ModelRef: pairTestRef, OutputPath: "/pulled.gguf"}, nil
	}
	ensureModelRunner = func(_ context.Context, opts modelrunner.EnsureOptions) (modelrunner.Record, error) {
		p.steps, p.runner = append(p.steps, "runner"), opts
		if p.failAt == "runner" {
			return modelrunner.Record{}, errors.New("runner failed")
		}
		return modelrunner.Record{ModelRef: pairTestRef, Key: "runner-key", Host: "127.0.0.1", Port: 31000, PID: 12345, Engine: "custom"}, nil
	}
	attachModelService = func(_ context.Context, opts Options) (Attachment, error) {
		p.steps, p.service = append(p.steps, "attach"), opts
		if p.failAt == "attach" {
			return Attachment{}, errors.New("attachment failed")
		}
		return Attachment{Target: "127.0.0.1:32000", Mode: opts.Mode, PID: 23456, Port: 32000}, nil
	}
	releaseModelRunner = func(_, ref, holder string) error {
		p.steps = append(p.steps, "release-runner:"+holder)
		if ref != pairTestRef {
			t.Errorf("released model = %q", ref)
		}
		return nil
	}
	releaseModelService = func(_, name string) error {
		p.steps = append(p.steps, "release-service:"+name)
		return nil
	}
	return p
}

func pairTestOptions(t *testing.T) workspace.Options {
	t.Helper()
	return workspace.Options{
		Name: "paired", StateDir: t.TempDir(), Model: "test/model/model.gguf",
		ModelRunner: workspace.ModelRunnerSpec{Command: []string{"fake-runner", "{model}", "--listen", "{addr}"}},
		Env:         map[string]string{"KEEP": "original"},
	}
}

func TestPairWiresBothBackendsAndReleasesCapturedHolder(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
		for _, mode := range []string{"", "policy"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				p := stubPairing(t)
				opts := pairTestOptions(t)
				opts.Backend = backend
				if mode != "" {
					opts.ModelMediation = workspace.ModelMediationSpec{Mode: mode, PolicyURL: "http://127.0.0.1:31001/decision"}
				}
				originalEnv := opts.Env
				release, err := Pair(t.Context(), &opts, PairOptions{Token: "transient-test-token", ExecPath: "/trusted/microagent"})
				if err != nil {
					t.Fatal(err)
				}
				if opts.Model != pairTestRef || opts.Env["KEEP"] != "original" || opts.Env["OPENAI_BASE_URL"] != "http://127.0.0.1:11434/v1" || opts.Env["MICROAGENT_MODEL_URL"] != opts.Env["OPENAI_BASE_URL"] {
					t.Fatalf("pairing = %+v", opts)
				}
				if len(originalEnv) != 1 {
					t.Fatalf("mutated caller map: %v", originalEnv)
				}
				if p.pull.Token != "transient-test-token" || p.runner.Holder != opts.Name || p.service.ExecPath != "/trusted/microagent" || p.service.Mode != mode {
					t.Fatalf("pairing parameters = %+v", p)
				}
				req, err := workspace.Request(opts, "run", "/rootfs.ext4", "request")
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, listener := range req.Config.VsockListeners {
					if listener.Port != workspace.DefaultModelVsockPort {
						continue
					}
					found = true
					if listener.Target != "127.0.0.1:32000" || listener.ModelRef != "" || listener.ModelRunnerKey != "" {
						t.Fatalf("unsafe listener: %+v", listener)
					}
				}
				if !found || opts.ModelTargetMediated != (mode != "") {
					t.Fatal("missing model attachment or incorrect mediation state")
				}
				if err := workspace.WriteManifest(opts); err != nil {
					t.Fatal(err)
				}
				manifest, err := workspace.ReadManifest(opts.StateDir, opts.Name)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "transient-test-token") {
					t.Fatal("pull token persisted")
				}
				opts.Name = "another-workspace"
				release()
				want := []string{"find", "pull", "runner", "attach", "release-service:paired", "release-runner:paired"}
				if !reflect.DeepEqual(p.steps, want) {
					t.Fatalf("steps = %v", p.steps)
				}
			})
		}
	}
}

func TestPairRollsBackWithoutMutatingOptions(t *testing.T) {
	for _, failure := range []string{"pull", "runner", "attach", "event"} {
		t.Run(failure, func(t *testing.T) {
			p := stubPairing(t)
			p.failAt = failure
			opts := pairTestOptions(t)
			original := opts
			if failure == "event" {
				dir := filepath.Join(opts.StateDir, opts.Name)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "events.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			release, err := Pair(t.Context(), &opts, PairOptions{ExecPath: "/trusted/microagent"})
			if err == nil || release != nil {
				t.Fatalf("failed pairing = %v, has cleanup %v", err, release != nil)
			}
			if !reflect.DeepEqual(original, opts) || len(original.Env) != 1 {
				t.Fatal("failed pairing mutated caller options")
			}
			steps := strings.Join(p.steps, ",")
			if strings.Contains(steps, "release-runner") != (failure == "attach" || failure == "event") {
				t.Fatalf("runner rollback = %s", steps)
			}
			if strings.Contains(steps, "release-service") != (failure == "event") {
				t.Fatalf("service rollback = %s", steps)
			}
		})
	}
}

func TestPairValidatesBeforeSideEffects(t *testing.T) {
	for _, problem := range []string{"no-model", "bad-name", "no-executable", "bad-policy"} {
		t.Run(problem, func(t *testing.T) {
			p := stubPairing(t)
			opts := pairTestOptions(t)
			config := PairOptions{ExecPath: "/trusted/microagent"}
			switch problem {
			case "no-model":
				opts.Model = ""
			case "bad-name":
				opts.Name = "../escape"
			case "no-executable":
				config.ExecPath = ""
			case "bad-policy":
				opts.ModelMediation.Mode = "policy"
			}
			release, err := Pair(t.Context(), &opts, config)
			if problem == "no-model" {
				if err != nil || release == nil {
					t.Fatalf("empty pairing: %v", err)
				}
				release()
			} else if err == nil {
				t.Fatal("invalid pairing accepted")
			}
			if len(p.steps) != 0 {
				t.Fatalf("unexpected side effects: %v", p.steps)
			}
		})
	}
}

func TestPairUsesCacheAndEnvironmentToken(t *testing.T) {
	p := stubPairing(t)
	t.Setenv("HF_TOKEN", "environment-test-token")
	opts := pairTestOptions(t)
	release, err := Pair(t.Context(), &opts, PairOptions{ExecPath: "/trusted/microagent"})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if p.pull.Token != "environment-test-token" {
		t.Fatal("environment token was not passed to pull")
	}
	p.cached, p.steps = true, nil
	release, err = Pair(t.Context(), &opts, PairOptions{ExecPath: "/trusted/microagent"})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if strings.Contains(strings.Join(p.steps, ","), "pull") || p.runner.ModelPath != "/cached.gguf" {
		t.Fatalf("cache path = %+v", p)
	}
}
