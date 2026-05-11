package models

import "testing"

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
