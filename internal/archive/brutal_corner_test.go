package archive

// Corner-case additions for the request-archive writer: file-name formatting,
// collision behavior, and directory handling (trailing slash, relative,
// unicode/spaces, deletion-while-running).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus/hooks/test"
)

// countJSONFiles returns the .json count in dir, 0 when dir is missing:
// polling a lazily created directory must not fatal like jsonFiles does.
func countJSONFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// waitForJSONFiles polls until dir holds n json files or the deadline passes;
// the writer goroutine is asynchronous, so tests that must sequence writes
// against filesystem effects poll instead of Close.
func waitForJSONFiles(t *testing.T, dir string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for countJSONFiles(dir) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d archive files in %s", n, dir)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The millisecond part of the file name is always three digits (007, not 7),
// keeping names sortable and fixed-width.
func TestFileNameMillisecondPadding(t *testing.T) {
	base := time.Date(2026, 9, 18, 14, 25, 30, 0, time.Local)
	cases := []struct {
		ms   int
		want string
	}{
		{0, "20260918T142530-000_req-1.json"},
		{7, "20260918T142530-007_req-1.json"},
		{99, "20260918T142530-099_req-1.json"},
		{123, "20260918T142530-123_req-1.json"},
		{999, "20260918T142530-999_req-1.json"},
	}
	for _, c := range cases {
		ts := base.Add(time.Duration(c.ms) * time.Millisecond)
		if got := fileName(Record{Timestamp: ts, RequestID: "req-1"}); got != c.want {
			t.Errorf("ms=%d: fileName = %q, want %q", c.ms, got, c.want)
		}
	}
}

// Two records stamped in the same millisecond (distinct request ids) must not
// collide: both files are written and both parse.
func TestWriterSameMillisecondDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	lg, _ := test.NewNullLogger()
	w := New(lg)
	ts := time.Date(2026, 9, 18, 14, 25, 30, 123000000, time.Local)
	for _, id := range []string{"req-1", "req-2"} {
		w.Record(Record{Dir: dir, Timestamp: ts, RequestID: id,
			Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	}
	w.Close()
	files := jsonFiles(t, dir)
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(files), files)
	}
}

// A trailing slash in the directory must not break path construction.
func TestWriterTrailingSlashDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "arch") + "/"
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	w.Close()
	if got := jsonFiles(t, dir); len(got) != 1 {
		t.Fatalf("got %d files under trailing-slash dir, want 1", len(got))
	}
}

// A relative dir resolves against the process working directory (the writer
// never chdirs); pin the contract so a change is noticed.
func TestWriterRelativeDirResolvedAgainstCWD(t *testing.T) {
	rel := "sapi-archive-reltest"
	defer os.RemoveAll(rel)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Record(Record{Dir: rel, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	w.Close()
	if got := countJSONFiles(filepath.Join(wd, rel)); got != 1 {
		t.Fatalf("files under $PWD/%s = %d, want 1 (relative dir is CWD-relative)", rel, got)
	}
}

// Unicode and spaces are legal in POSIX paths and must work unquoted.
func TestWriterUnicodeAndSpacesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "存档 目录 with spaces")
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{"ok":true}`)})
	w.Close()
	if got := jsonFiles(t, dir); len(got) != 1 {
		t.Fatalf("got %d files under unicode dir, want 1", len(got))
	}
}

// Deleting the directory while the writer lives loses nothing: MkdirAll runs
// per write, so the next record lazily recreates it.
func TestWriterDirRecreatedAfterDeletion(t *testing.T) {
	dir := t.TempDir()
	lg, _ := test.NewNullLogger()
	w := New(lg)
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-1",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	waitForJSONFiles(t, dir, 1)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	w.Record(Record{Dir: dir, Timestamp: time.Now(), RequestID: "req-2",
		Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	waitForJSONFiles(t, dir, 1)
	w.Close()
	if got := jsonFiles(t, dir); len(got) != 1 {
		t.Fatalf("got %d files after recreation, want 1 (only req-2)", len(got))
	}
}
