package validation

import (
	"errors"
	"testing"
)

func TestParseScore(t *testing.T) {
	cases := []struct {
		in       string
		want     Score
		wantErr  error
		wantOK   bool
	}{
		{"3-1", Score{3, 1}, nil, true},
		{"0-3", Score{0, 3}, nil, true},
		{"11-9", Score{11, 9}, nil, true},
		{"99-0", Score{99, 0}, nil, true},
		{"100-1", Score{}, ErrScoreRange, false},
		{"3-3", Score{}, ErrScoreTie, false},
		{"-1-2", Score{}, ErrScoreFormat, false},
		{"3- 1", Score{}, ErrScoreFormat, false},
		{"03-1", Score{}, ErrScoreFormat, false},
		{"3.0-1", Score{}, ErrScoreFormat, false},
		{"abc", Score{}, ErrScoreFormat, false},
		{"", Score{}, ErrScoreFormat, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseScore(c.in)
			if c.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != c.want {
					t.Fatalf("got %+v; want %+v", got, c.want)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got err %v; want %v", err, c.wantErr)
			}
		})
	}
}

func TestParseIdentifier(t *testing.T) {
	cases := []struct {
		in     string
		isTG   bool
		value  string
		wantOK bool
	}{
		{"@alice123", true, "alice123", true},
		{"@a", false, "", false},      // too short
		{"@1abc", false, "", false},   // can't start with digit
		{"@al-ice", false, "", false}, // dash not allowed in tg
		{"alice123", false, "alice123", true},
		{"john_doe", false, "john_doe", true},
		{"", false, "", false},
		{"@", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseIdentifier(c.in)
			if c.wantOK {
				if err != nil {
					t.Fatalf("unexpected err %v", err)
				}
				if got.IsTelegram != c.isTG || got.Value != c.value {
					t.Fatalf("got %+v; want isTG=%v value=%q", got, c.isTG, c.value)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got %+v", got)
			}
		})
	}
}

func TestExtractMatchID(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"#42", 42, true},
		{"/undo #42", 42, true},
		{"please undo #1 quickly", 1, true},
		{"reply to: Match #99 pending: ...", 99, true},
		{"no id here", 0, false},
		{"#", 0, false},
		{"#abc", 0, false},
		{"prefix#42", 42, true}, // technically captures, fine
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := ExtractMatchID(c.in)
			if ok != c.ok {
				t.Fatalf("ok=%v; want %v", ok, c.ok)
			}
			if got != c.want {
				t.Fatalf("got %d; want %d", got, c.want)
			}
		})
	}
}

func TestParseAdminCredentials(t *testing.T) {
	l, p, err := ParseAdminCredentials("john_doe:secret123")
	if err != nil || l != "john_doe" || p != "secret123" {
		t.Fatalf("got (%q,%q,%v); want (john_doe,secret123,nil)", l, p, err)
	}
	if _, _, err := ParseAdminCredentials("nocolon"); err == nil {
		t.Error("expected error for missing colon")
	}
	if _, _, err := ParseAdminCredentials(":password"); err == nil {
		t.Error("expected error for empty login")
	}
	if _, _, err := ParseAdminCredentials("login:"); err == nil {
		t.Error("expected error for empty password")
	}
	// Password may contain colons.
	l, p, err = ParseAdminCredentials("login:pass:word")
	if err != nil || l != "login" || p != "pass:word" {
		t.Fatalf("password with colon: got (%q,%q,%v)", l, p, err)
	}
}
