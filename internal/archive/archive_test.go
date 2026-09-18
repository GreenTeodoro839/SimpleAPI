package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func sampleRecord(dir string) Record {
	ts := time.Date(2026, 9, 18, 14, 25, 30, 123000000, time.Local)
	return Record{
		Dir:           dir,
		Timestamp:     ts,
		RequestID:     "req-42",
		APIKey:        "dev-all",
		Model:         "claude",
		InternalModel: "anthropic-main/sonnet4",
		ProviderType:  "anthropic",
		Tokens:        Tokens{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		Request:       json.RawMessage(`{"model":"claude","messages":[]}`),
		Response:      json.RawMessage(`{"id":"x","model":"claude"}`),
	}
}

func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	return names
}

func warned(hook *test.Hook, substr string) bool {
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel && strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}

// jsonSemanticEqual compares two JSON documents by value: MarshalIndent
// re-indents embedded RawMessage, so byte equality does not hold for written
// files, but content must match exactly.
func jsonSemanticEqual(t *testing.T, got, want []byte, what string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: archived JSON does not parse: %v (%s)", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: expected JSON does not parse: %v (%s)", what, err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: got %s, want %s", what, got, want)
	}
}

func TestWriterCreatesFileWithContents(t *testing.T) {
	dir := t.TempDir()
	lg, _ := test.NewNullLogger()
	w := New(lg)
	rec := sampleRecord(dir)
	w.Record(rec)
	w.Close()

	files := jsonFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1: %v", len(files), files)
	}
	if want := "20260918T142530-123_req-42.json"; files[0] != want {
		t.Errorf("file name = %q, want %q", files[0], want)
	}
	data, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	if got.Timestamp != rec.Timestamp || got.RequestID != "req-42" || got.APIKey != "dev-all" ||
		got.Model != "claude" || got.InternalModel != "anthropic-main/sonnet4" || got.ProviderType != "anthropic" {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if got.Tokens != (Tokens{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}) {
		t.Errorf("tokens mismatch: %+v", got.Tokens)
	}
	jsonSemanticEqual(t, got.Request, []byte(`{"model":"claude","messages":[]}`), "request")
	jsonSemanticEqual(t, got.Response, []byte(`{"id":"x","model":"claude"}`), "response")
	if got.ResponseStream != nil {
		t.Errorf("response_stream = %q, want nil", *got.ResponseStream)
	}
	if !strings.Contains(string(data), "\n  ") {
		t.Error("output is not indented")
	}
	fi, err := os.Stat(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestWriterStreamRecordShape(t *testing.T) {
	dir := t.TempDir()
	lg, _ := test.NewNullLogger()
	w := New(lg)
	sse := "data: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n"
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), ResponseStream: &sse})
	w.Close()

	files := jsonFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	data, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["response"]) != "null" {
		t.Errorf("response = %s, want null", raw["response"])
	}
	var rs string
	if err := json.Unmarshal(raw["response_stream"], &rs); err != nil || rs != sse {
		t.Errorf("response_stream = %q err = %v", rs, err)
	}
}

func TestWriterCreatesDirLazily(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	w.Close()
	if files := jsonFiles(t, dir); len(files) != 1 {
		t.Fatalf("got %d files in lazily created dir, want 1", len(files))
	}
}

func TestRecordDropsWhenQueueFull(t *testing.T) {
	lg, hook := test.NewNullLogger()
	// Hand-constructed writer: 1-slot queue, no consumer goroutine.
	w := &Writer{logger: lg, ch: make(chan Record, 1), done: make(chan struct{})}
	w.Record(Record{RequestID: "req-1"})
	w.Record(Record{RequestID: "req-2"}) // must be dropped, not block
	if len(w.ch) != 1 {
		t.Fatalf("queue len = %d, want 1", len(w.ch))
	}
	if !warned(hook, "queue full") {
		t.Error("expected a queue-full warning")
	}
	close(w.done)
}

func TestCloseDrainsPending(t *testing.T) {
	dir := t.TempDir()
	lg, _ := test.NewNullLogger()
	w := New(lg)
	for i := 0; i < 3; i++ {
		w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: fmt.Sprintf("req-%d", i),
			Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	}
	w.Close() // returns only after all three are written
	if files := jsonFiles(t, dir); len(files) != 3 {
		t.Fatalf("got %d files after Close, want 3", len(files))
	}
}

func TestWriteErrorLogsAndDrops(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lg, hook := test.NewNullLogger()
	w := New(lg)
	// MkdirAll must fail: a path component is a regular file.
	w.Record(Record{Dir: filepath.Join(blocker, "sub"), Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	w.Close()
	if !warned(hook, "mkdir failed") {
		t.Error("expected a mkdir-failed warning")
	}
}

func TestRecordAfterCloseDoesNotPanic(t *testing.T) {
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Close()
	w.Record(Record{RequestID: "req-1"}) // silently dropped
}
