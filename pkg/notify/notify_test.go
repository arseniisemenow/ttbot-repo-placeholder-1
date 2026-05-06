package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
)

func TestDMSucceeds(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	user := models.User{TelegramID: 100, DMChatID: 100, TelegramUsername: "alice"}
	if err := n.SendUser(context.Background(), user, models.Group{}, "hello"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || calls[0].ChatID != 100 || calls[0].TopicID != 0 || calls[0].Text != "hello" {
		t.Fatalf("expected 1 DM, got %+v", calls)
	}
}

func TestFallbackToGroupOnForbidden(t *testing.T) {
	mock := messenger.NewMock()
	mock.FailNextForChat("SendMessage", 100, messenger.ErrForbidden)
	n := New(mock)
	user := models.User{TelegramID: 100, DMChatID: 100, TelegramUsername: "alice"}
	group := models.Group{GroupID: -1001, MatchesTopicID: 5}
	if err := n.SendUser(context.Background(), user, group, "hello"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected DM+fallback, got %+v", calls)
	}
	if calls[1].ChatID != -1001 || calls[1].TopicID != 5 || calls[1].Text != "@alice, hello" {
		t.Fatalf("fallback wrong: %+v", calls[1])
	}
}

func TestFallbackWhenNoDMChatID(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	user := models.User{TelegramID: 100, TelegramUsername: "alice"} // no DMChatID
	group := models.Group{GroupID: -1001, MatchesTopicID: 5}
	if err := n.SendUser(context.Background(), user, group, "hello"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || calls[0].ChatID != -1001 {
		t.Fatalf("expected single fallback message, got %+v", calls)
	}
}

func TestNoDeliveryPossible(t *testing.T) {
	mock := messenger.NewMock()
	n := New(mock)
	user := models.User{TelegramID: 100} // no DM, no username
	if err := n.SendUser(context.Background(), user, models.Group{}, "hello"); err == nil {
		t.Fatal("expected error when no delivery channel available")
	}
}

func TestPropagatesUnexpectedDMError(t *testing.T) {
	mock := messenger.NewMock()
	custom := errors.New("network down")
	mock.FailNextForChat("SendMessage", 100, custom)
	n := New(mock)
	user := models.User{TelegramID: 100, DMChatID: 100}
	group := models.Group{GroupID: -1001, MatchesTopicID: 5}
	if err := n.SendUser(context.Background(), user, group, "hello"); !errors.Is(err, custom) {
		t.Fatalf("expected %v, got %v", custom, err)
	}
}
