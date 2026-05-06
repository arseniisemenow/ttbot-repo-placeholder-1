package models

import "testing"

func TestUserHasNickname(t *testing.T) {
	cases := []struct {
		status NicknameStatus
		want   bool
	}{
		{NicknameStatusNone, false},
		{NicknameStatusProvided, true},
		{NicknameStatusGuest, true},
	}
	for _, c := range cases {
		u := User{NicknameStatus: c.status}
		if got := u.HasNickname(); got != c.want {
			t.Errorf("HasNickname for %q = %v; want %v", c.status, got, c.want)
		}
	}
}

func TestUserIsVerified(t *testing.T) {
	cases := []struct {
		v    VerifiedBy
		want bool
	}{
		{"", false},
		{VerifiedByNone, false},
		{VerifiedByAdmin, true},
		{VerifiedByAuth, true},
	}
	for _, c := range cases {
		u := User{VerifiedBy: c.v}
		if got := u.IsVerified(); got != c.want {
			t.Errorf("IsVerified for %q = %v; want %v", c.v, got, c.want)
		}
	}
}

func TestUserDisplayName(t *testing.T) {
	cases := []struct {
		name string
		u    User
		want string
	}{
		{"nicknamed_uses_s21", User{NicknameStatus: NicknameStatusProvided, S21Nickname: "alice", TelegramUsername: "al"}, "alice"},
		{"guest_uses_at_username", User{NicknameStatus: NicknameStatusGuest, TelegramUsername: "bob"}, "@bob"},
		{"none_with_username", User{NicknameStatus: NicknameStatusNone, TelegramUsername: "x"}, "@x"},
		{"none_no_username", User{NicknameStatus: NicknameStatusNone}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.u.DisplayName(); got != c.want {
				t.Errorf("DisplayName=%q; want %q", got, c.want)
			}
		})
	}
}

func TestMatchWinnerLoser(t *testing.T) {
	m := Match{Player1ID: 1, Player2ID: 2, Player1Score: 3, Player2Score: 1}
	if m.Winner() != 1 || m.Loser() != 2 {
		t.Errorf("p1 wins: got winner=%d loser=%d", m.Winner(), m.Loser())
	}
	m = Match{Player1ID: 1, Player2ID: 2, Player1Score: 1, Player2Score: 3}
	if m.Winner() != 2 || m.Loser() != 1 {
		t.Errorf("p2 wins: got winner=%d loser=%d", m.Winner(), m.Loser())
	}
	m = Match{Player1ID: 1, Player2ID: 2, Player1Score: 3, Player2Score: 3}
	if m.Winner() != 0 || m.Loser() != 0 {
		t.Errorf("tie: got winner=%d loser=%d", m.Winner(), m.Loser())
	}
}

func TestGroupFullyConfigured(t *testing.T) {
	if (Group{}).FullyConfigured() {
		t.Error("empty group should not be fully configured")
	}
	if (Group{MatchesTopicID: 1}).FullyConfigured() {
		t.Error("only matches topic = not fully configured")
	}
	if !(Group{MatchesTopicID: 1, StatsTopicID: 2}).FullyConfigured() {
		t.Error("both topics = fully configured")
	}
}
