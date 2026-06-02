package secret

import "testing"

func TestParseDotenv(t *testing.T) {
	in := []byte("# a comment\n\nFOO=bar\nexport BAZ='qux'\nQUOTED=\"with space\"\nEMPTY=\n")
	got, err := parseDotenv(in)
	if err != nil {
		t.Fatalf("parseDotenv error: %v", err)
	}
	want := map[string]string{"FOO": "bar", "BAZ": "qux", "QUOTED": "with space", "EMPTY": ""}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d (%v)", len(got), len(want), got)
	}
}

func TestParseDotenvRejectsNonKeyValueLine(t *testing.T) {
	if _, err := parseDotenv([]byte("this is not valid\n")); err == nil {
		t.Fatal("expected error for non KEY=VALUE line")
	}
}

func TestParseDotenvRejectsEmptyKey(t *testing.T) {
	if _, err := parseDotenv([]byte("=novalue\n")); err == nil {
		t.Fatal("expected error for empty key")
	}
}
