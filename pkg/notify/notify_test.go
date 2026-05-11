package notify

import (
	"context"
	"testing"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
)

func TestSendInGroupWithUsername(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	g := models.Group{GroupID: -1001, MatchesTopicID: 5}
	if err := n.SendInGroup(context.Background(), g, "alice", "hello"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || calls[0].ChatID != -1001 || calls[0].TopicID != 5 || calls[0].Text != "@alice, hello" {
		t.Fatalf("got %+v", calls)
	}
}

func TestSendInGroupNoUsername(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	g := models.Group{GroupID: -1001, MatchesTopicID: 5}
	if err := n.SendInGroup(context.Background(), g, "", "hello"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || calls[0].Text != "hello" {
		t.Fatalf("expected un-prefixed text, got %+v", calls)
	}
}

func TestSendInGroupNoMatchesTopic(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	g := models.Group{GroupID: -1001} // no matches topic
	if err := n.SendInGroup(context.Background(), g, "alice", "hello"); err == nil {
		t.Fatal("expected error when matches topic is unset")
	}
	if calls := mock.Calls(); len(calls) != 0 {
		t.Errorf("expected no sends, got %+v", calls)
	}
}

func TestSendInGroupNoGroup(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	if err := n.SendInGroup(context.Background(), models.Group{}, "alice", "hello"); err == nil {
		t.Fatal("expected error for zero group")
	}
}
