package calllog

import "testing"

func TestRecorder_RingOverwriteAndOrder(t *testing.T) {
	r := NewRecorder(3)
	if !r.Enabled() {
		t.Fatal("capacity 3 should be enabled")
	}
	for i := 0; i < 5; i++ {
		r.Record(Entry{RequestID: "req", HTTPStatus: 100 + i})
	}
	got := r.Recent(10)
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3 (buffer cap)", len(got))
	}
	// Newest first: last recorded is index 0.
	want := []int{104, 103, 102}
	for i, e := range got {
		if e.HTTPStatus != want[i] {
			t.Fatalf("pos %d: got %d want %d", i, e.HTTPStatus, want[i])
		}
	}

	// limit clamp.
	if len(r.Recent(2)) != 2 {
		t.Fatalf("limit=2 should clamp to 2")
	}
}

func TestRecorder_Disabled(t *testing.T) {
	r := NewRecorder(0)
	if r.Enabled() {
		t.Fatal("capacity 0 should be disabled")
	}
	r.Record(Entry{RequestID: "x"})
	if got := r.Recent(10); got != nil {
		t.Fatalf("disabled recorder should return nil, got %v", got)
	}
}
