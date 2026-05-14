// Package messenger is the abstraction layer that hides the messenger SDK
// from the rest of the bot. Production code outside this package must not
// import the Telegram SDK.
package messenger

import (
	"context"
	"errors"
)

// Messenger is the interface used by handlers and the periodic job.
//
// Topics: Telegram supergroup forum threads carry an int64 message_thread_id.
// 0 means "no thread" (General topic). When the handlers need to send into a
// specific topic (matches topic, stats topic), they pass topicID > 0.
type Messenger interface {
	// SendMessage sends plain text. If topicID > 0, sent inside that forum thread.
	// Returns the new message's ID.
	SendMessage(ctx context.Context, chatID, topicID int64, text string) (int64, error)

	// SendMessageWithForceReply sends a DM tagged with Telegram's
	// `force_reply` markup. The recipient's client shows an immediate
	// reply composer; their reply arrives as a Message with ReplyTo
	// populated. Used to split a sensitive prompt ("/admin") from the
	// secret ("login:password") into two messages so the bot can delete
	// only the secret.
	SendMessageWithForceReply(ctx context.Context, chatID int64, text, placeholder string) (int64, error)

	// SendKeyboard sends a message with two inline buttons (typical Approve/Cancel
	// pattern used for /match confirmation). callbackData* may be any string up
	// to ~64 bytes (Telegram limit).
	SendKeyboard(ctx context.Context, chatID, topicID int64, text, leftLabel, leftCallback, rightLabel, rightCallback string) (int64, error)

	// SendInlineKeyboard sends a message with N inline buttons stacked one per
	// row. Used when the caller doesn't know the button count ahead of time —
	// e.g. "pick a group" prompts.
	SendInlineKeyboard(ctx context.Context, chatID, topicID int64, text string, buttons []Button) (int64, error)

	// EditMessage replaces the text of an existing message.
	EditMessage(ctx context.Context, chatID, messageID int64, text string) error

	// EditKeyboard replaces the inline keyboard of an existing message.
	// buttons is a list of (label, callbackData) pairs, rendered in one row.
	// Pass nil/empty to remove the keyboard.
	EditKeyboard(ctx context.Context, chatID, messageID int64, text string, buttons []Button) error

	// DeleteMessage removes a message.
	DeleteMessage(ctx context.Context, chatID, messageID int64) error

	// PinMessage pins a message in its chat (or its topic, in supergroups).
	PinMessage(ctx context.Context, chatID, messageID int64) error

	// AnswerCallback acknowledges a button tap. text is shown as a transient
	// notification to the tapper (≤200 chars) — pass "" for silent ack.
	AnswerCallback(ctx context.Context, callbackQueryID, text string) error

	// SendReaction emits an emoji reaction on a specific message.
	SendReaction(ctx context.Context, chatID, messageID int64, emoji string) error

	// LeaveChat makes the bot leave a chat.
	LeaveChat(ctx context.Context, chatID int64) error

	// IsChatAdmin reports whether the given user is an administrator (or the
	// creator) of the given chat. Used for group-config commands and
	// match-flow auto-approve where "admin" is now Telegram-chat-admin, not
	// a separately-stored campus admin role.
	IsChatAdmin(ctx context.Context, chatID, userID int64) (bool, error)

	// ResolveChatMemberUsername fetches the current @username for a chat
	// member via Telegram's getChatMember endpoint. Returns "" when the user
	// has no public @username (or when Telegram doesn't include the field
	// for non-member statuses). Errors are reserved for transport failures.
	ResolveChatMemberUsername(ctx context.Context, chatID, userID int64) (string, error)
}

// Button is a single inline-keyboard button.
type Button struct {
	Label    string
	Callback string
}

// Common errors. Adapters translate platform-specific errors into these.
var (
	// ErrForbidden is returned when the bot cannot reach the user (typically a
	// 403 from Telegram, e.g. user has not started the bot, or has blocked it).
	// Used by the two-step notification helper to fall back to group chat.
	ErrForbidden = errors.New("messenger: forbidden")
	// ErrNotFound is returned when a chat or message cannot be located.
	ErrNotFound = errors.New("messenger: not found")
)

// Update wraps an inbound Telegram update — only the subset the bot uses.
type Update struct {
	UpdateID      int64
	Message       *Message
	CallbackQuery *CallbackQuery
	MyChatMember  *ChatMemberUpdate
	ChatMember    *ChatMemberUpdate
}

// Message is one Telegram message.
type Message struct {
	MessageID       int64
	Chat            Chat
	From            *User
	Text            string
	MessageThreadID int64    // 0 if outside a forum thread
	ReplyTo         *Message // bot's matched-message reply for /undo

	// ForwardFrom is the original sender of a forwarded message. Nil unless
	// this message is a forward from a user whose forward-privacy allows
	// disclosing their account. The DM-forward → participants backfill flow
	// relies on this field.
	ForwardFrom *User
	// ForwardSenderName is set instead of ForwardFrom when the original sender
	// has hidden their account on forwards. We cannot recover their id in that
	// case; this field is present only so the handler can surface a helpful
	// "this user has hidden their account" reply.
	ForwardSenderName string
}

// CallbackQuery is an inline-keyboard button tap.
type CallbackQuery struct {
	ID      string
	From    *User
	Message *Message
	Data    string
}

// ChatMemberUpdate is a join/leave/promotion event.
type ChatMemberUpdate struct {
	Chat          Chat
	From          *User // who triggered (often the new member themselves)
	NewChatMember *ChatMember
	OldChatMember *ChatMember
}

// ChatMember describes a user's role in a chat.
type ChatMember struct {
	User   *User
	Status string // "member", "administrator", "creator", "left", "kicked"
}

// Chat is a chat reference.
type Chat struct {
	ID       int64
	Type     string // "private", "group", "supergroup", "channel"
	Title    string
	IsForum  bool
	Username string
}

// User is a Telegram user.
type User struct {
	ID        int64
	IsBot     bool
	Username  string
	FirstName string
	LastName  string
}
