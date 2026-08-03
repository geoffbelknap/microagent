package workspace

import "testing"

func TestCallerContextPersistsThroughManifest(t *testing.T) {
	opts := DefaultOptions()
	opts.StateDir = t.TempDir()
	opts.Name = "agent"
	opts.Purpose = "  caller text is opaque  "
	opts.CorrelationID = "task/42"
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(opts.StateDir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	restored := OptionsFromManifest(DefaultOptions(), manifest)
	if restored.Purpose != opts.Purpose || restored.CorrelationID != opts.CorrelationID {
		t.Fatalf("restored caller context = %#v", restored)
	}
}
