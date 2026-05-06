// Package validation parses and validates user-supplied command tokens.
package validation

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Score holds a parsed match score.
type Score struct {
	P1, P2 uint32
}

// Identifier is a parsed player reference: either a Telegram @username or an
// S21 nickname.
type Identifier struct {
	IsTelegram  bool   // true → match by telegram_username, false → match by s21_nickname
	Value       string // without leading @ for telegram, raw nickname for s21
}

var (
	// ScoreRegex enforces "<digits>-<digits>" with no leading zeros (except "0"
	// itself), no signs, no decimals.
	ScoreRegex = regexp.MustCompile(`^(0|[1-9][0-9]*)-(0|[1-9][0-9]*)$`)

	// MatchIDRegex extracts a match id like "#42" from arbitrary text. It captures
	// the digits.
	MatchIDRegex = regexp.MustCompile(`#(\d+)`)

	// usernameRegex validates a Telegram username (5–32 chars, alnum + underscore).
	usernameRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

	// s21NicknameRegex is a permissive S21 login matcher (alnum + dash/underscore).
	s21NicknameRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// Errors returned by ParseScore.
var (
	ErrScoreFormat = errors.New("Score must look like `3-1` (non-negative integers, ≤99, not equal)")
	ErrScoreTie    = errors.New("Score must have a winner")
	ErrScoreRange  = errors.New("Score values must be between 0 and 99")
)

// ScoreMax caps score values to catch typos (table tennis sets rarely exceed 30).
const ScoreMax = 99

// ParseScore validates and parses a score token like "3-1".
func ParseScore(token string) (Score, error) {
	m := ScoreRegex.FindStringSubmatch(token)
	if m == nil {
		return Score{}, ErrScoreFormat
	}
	p1, err1 := strconv.ParseUint(m[1], 10, 32)
	p2, err2 := strconv.ParseUint(m[2], 10, 32)
	if err1 != nil || err2 != nil {
		return Score{}, ErrScoreFormat
	}
	if p1 > ScoreMax || p2 > ScoreMax {
		return Score{}, ErrScoreRange
	}
	if p1 == p2 {
		return Score{}, ErrScoreTie
	}
	return Score{P1: uint32(p1), P2: uint32(p2)}, nil
}

// ParseIdentifier resolves a single token to either a Telegram username
// reference (if it begins with @) or an S21 nickname.
func ParseIdentifier(token string) (Identifier, error) {
	if token == "" {
		return Identifier{}, errors.New("empty identifier")
	}
	if strings.HasPrefix(token, "@") {
		name := strings.TrimPrefix(token, "@")
		if !usernameRegex.MatchString(name) {
			return Identifier{}, errors.New("invalid telegram username")
		}
		return Identifier{IsTelegram: true, Value: name}, nil
	}
	if !s21NicknameRegex.MatchString(token) {
		return Identifier{}, errors.New("invalid s21 nickname")
	}
	return Identifier{IsTelegram: false, Value: token}, nil
}

// ExtractMatchID returns the first "#<digits>" id in the given text, or 0 if
// none. Used for both `/undo #42` arguments and `/undo` replies.
func ExtractMatchID(text string) (uint64, bool) {
	m := MatchIDRegex.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ParseAdminCredentials splits a "login:password" string. Login may contain
// underscores, dashes, dots; everything before the FIRST colon is the login.
func ParseAdminCredentials(token string) (login, password string, err error) {
	idx := strings.IndexByte(token, ':')
	if idx <= 0 || idx == len(token)-1 {
		return "", "", errors.New("expected login:password")
	}
	return token[:idx], token[idx+1:], nil
}
