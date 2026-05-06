package messenger

import (
	"context"
	"errors"
	"testing"
)

func TestMockRecordsAndReports(t *testing.T) {
	ctx := context.Background()
	m := NewMock()

	id, err := m.SendMessage(ctx, 100, 5, "hi")
	if err != nil || id != 1 {
		t.Fatalf("SendMessage: id=%d err=%v", id, err)
	}
	if err := m.EditMessage(ctx, 100, id, "hi (edited)"); err != nil {
		t.Fatal(err)
	}
	calls := m.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d:\n%s", len(calls), m.Pretty())
	}
	if calls[0].Method != "SendMessage" || calls[1].Method != "EditMessage" {
		t.Errorf("methods: %+v", calls)
	}
}

func TestMockFailNext(t *testing.T) {
	ctx := context.Background()
	m := NewMock()
	want := errors.New("boom")
	m.FailNext("SendMessage", want)
	if _, err := m.SendMessage(ctx, 1, 0, "x"); !errors.Is(err, want) {
		t.Fatalf("want injected error, got %v", err)
	}
	// Second call should succeed (failure is one-shot).
	if _, err := m.SendMessage(ctx, 1, 0, "y"); err != nil {
		t.Fatalf("expected success after one-shot fail, got %v", err)
	}
}

func TestMockFailNextForChat(t *testing.T) {
	ctx := context.Background()
	m := NewMock()
	m.FailNextForChat("SendMessage", 200, ErrForbidden)

	// Different chat should succeed.
	if _, err := m.SendMessage(ctx, 100, 0, "x"); err != nil {
		t.Fatalf("chat 100 should succeed: %v", err)
	}
	// Target chat fails.
	if _, err := m.SendMessage(ctx, 200, 0, "y"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("chat 200 want ErrForbidden, got %v", err)
	}
}

func TestMockCallsByMethod(t *testing.T) {
	ctx := context.Background()
	m := NewMock()
	_, _ = m.SendMessage(ctx, 1, 0, "a")
	_ = m.EditMessage(ctx, 1, 1, "b")
	_, _ = m.SendMessage(ctx, 2, 0, "c")
	if got := len(m.CallsByMethod("SendMessage")); got != 2 {
		t.Fatalf("SendMessage count = %d", got)
	}
	if got := len(m.CallsByMethod("EditMessage")); got != 1 {
		t.Fatalf("EditMessage count = %d", got)
	}
	if got := len(m.CallsByMethod("DeleteMessage")); got != 0 {
		t.Fatalf("DeleteMessage count = %d", got)
	}
}
