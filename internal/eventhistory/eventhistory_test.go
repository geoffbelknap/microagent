package eventhistory

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

type testEvent struct {
	ID    int    `json:"id"`
	State string `json:"state"`
}

func TestAppendSerializesConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	const writers = 24
	var wait sync.WaitGroup
	errorsSeen := make(chan error, writers)
	for id := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.Command(os.Args[0], "-test.run=TestAppendProcessHelper")
			command.Env = append(os.Environ(),
				"MICROAGENT_EVENT_HISTORY_HELPER=1",
				"MICROAGENT_EVENT_HISTORY_PATH="+path,
				fmt.Sprintf("MICROAGENT_EVENT_HISTORY_ID=%d", id),
			)
			if output, err := command.CombinedOutput(); err != nil {
				errorsSeen <- fmt.Errorf("writer %d: %w: %s", id, err, output)
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	events, err := Read[testEvent](path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("events = %d, want %d", len(events), writers)
	}
	seen := make(map[int]bool, writers)
	for _, event := range events {
		if seen[event.ID] {
			t.Fatalf("duplicate event ID %d in %#v", event.ID, events)
		}
		seen[event.ID] = true
	}
	for id := range writers {
		if !seen[id] {
			t.Errorf("event %d was lost", id)
		}
	}
}

func TestAppendProcessHelper(t *testing.T) {
	if os.Getenv("MICROAGENT_EVENT_HISTORY_HELPER") != "1" {
		return
	}
	id, err := strconv.Atoi(os.Getenv("MICROAGENT_EVENT_HISTORY_ID"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(
		os.Getenv("MICROAGENT_EVENT_HISTORY_PATH"),
		testEvent{ID: id, State: "running"},
		Options{},
	); err != nil {
		t.Fatal(err)
	}
}

func TestAppendRetainsDuplicatesInCommitOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	for _, id := range []int{1, 1, 2} {
		if err := Append(path, testEvent{ID: id}, Options{}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Read[testEvent](path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].ID != 1 || events[1].ID != 1 || events[2].ID != 2 {
		t.Fatalf("events = %#v, want duplicate-preserving commit order", events)
	}
}

func TestAppendAppliesBoundedRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	for id := range 5 {
		if err := Append(path, testEvent{ID: id}, Options{MaxEvents: 3}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Read[testEvent](path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].ID != 2 || events[2].ID != 4 {
		t.Fatalf("events = %#v, want last three", events)
	}
}

func TestAppendRejectsAndPreservesMalformedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	original := []byte(`[{"id":1}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Append(path, testEvent{ID: 2}, Options{})
	var integrity IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatalf("error = %T %v, want IntegrityError", err, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("malformed history changed: got %q want %q", got, original)
	}
}

func TestAppendWriteFailureLeavesCompletePriorHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	if err := Append(path, testEvent{ID: 1}, Options{}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	priorWriter := writeFileAtomic
	writeFileAtomic = func(string, []byte, os.FileMode) error {
		return errors.New("simulated interruption")
	}
	t.Cleanup(func() { writeFileAtomic = priorWriter })

	if err := Append(path, testEvent{ID: 2}, Options{}); err == nil {
		t.Fatal("expected simulated write failure")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("prior history changed after interrupted write: got %q want %q", got, original)
	}
	if _, err := Read[testEvent](path, Options{}); err != nil {
		t.Fatalf("prior history is no longer complete: %v", err)
	}
}

func TestAppendMigratesLegacyJSONLinesWhenAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(path, []byte("{\"id\":1}\n{\"id\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, testEvent{ID: 3}, Options{AllowJSONLines: true}); err != nil {
		t.Fatal(err)
	}
	events, err := Read[testEvent](path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].ID != 1 || events[2].ID != 3 {
		t.Fatalf("migrated events = %#v", events)
	}
}
