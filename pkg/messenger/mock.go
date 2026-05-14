package messenger

import (
	"context"
	"fmt"
	"sync"
)

// Call is a recorded invocation against the Mock messenger.
type Call struct {
	Method   string
	ChatID   int64
	TopicID  int64
	MessageID int64
	Text     string
	Buttons  []Button
	Emoji    string
	Callback string // for AnswerCallback
}

// Mock is a Messenger that records every call. It also lets tests inject
// failures and per-method message-ID counters.
type Mock struct {
	mu          sync.Mutex
	calls       []Call
	nextID      int64
	failures    map[string]error // method-name → error to return on next call
	failureChat map[string]int64 // optional chat-ID scoping for failures
	chatAdmins  map[chatAdminKey]bool
	chatMemberUsernames map[chatAdminKey]string
}

// NewMock creates a fresh Mock with messageID counter starting at 1.
func NewMock() *Mock {
	return &Mock{nextID: 0, failures: map[string]error{}, failureChat: map[string]int64{}}
}

// Calls returns a copy of all recorded calls.
func (m *Mock) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Call, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallsByMethod returns all recorded calls for the given method name.
func (m *Mock) CallsByMethod(method string) []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Call
	for _, c := range m.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// LastCall returns the most recent call or false if none.
func (m *Mock) LastCall() (Call, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return Call{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// Reset wipes recorded calls and counters.
func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.nextID = 0
}

// FailNext makes the next call to `method` return `err`. After firing once it
// is cleared.
func (m *Mock) FailNext(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[method] = err
}

// FailNextForChat is like FailNext but only fires when the call's chatID
// matches.
func (m *Mock) FailNextForChat(method string, chatID int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[method] = err
	m.failureChat[method] = chatID
}

func (m *Mock) maybeFail(method string, chatID int64) error {
	if err, ok := m.failures[method]; ok {
		want, scoped := m.failureChat[method]
		if !scoped || want == chatID {
			delete(m.failures, method)
			delete(m.failureChat, method)
			return err
		}
	}
	return nil
}

func (m *Mock) record(c Call) int64 {
	m.calls = append(m.calls, c)
	m.nextID++
	return m.nextID
}

func (m *Mock) SendMessage(ctx context.Context, chatID, topicID int64, text string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.record(Call{Method: "SendMessage", ChatID: chatID, TopicID: topicID, Text: text})
	if err := m.maybeFail("SendMessage", chatID); err != nil {
		return 0, err
	}
	return id, nil
}

func (m *Mock) SendMessageWithForceReply(ctx context.Context, chatID int64, text, placeholder string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.record(Call{Method: "SendMessageWithForceReply", ChatID: chatID, Text: text})
	if err := m.maybeFail("SendMessageWithForceReply", chatID); err != nil {
		return 0, err
	}
	return id, nil
}

func (m *Mock) SendKeyboard(ctx context.Context, chatID, topicID int64, text, leftLabel, leftCallback, rightLabel, rightCallback string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	buttons := []Button{{Label: leftLabel, Callback: leftCallback}, {Label: rightLabel, Callback: rightCallback}}
	id := m.record(Call{Method: "SendKeyboard", ChatID: chatID, TopicID: topicID, Text: text, Buttons: buttons})
	if err := m.maybeFail("SendKeyboard", chatID); err != nil {
		return 0, err
	}
	return id, nil
}

func (m *Mock) SendInlineKeyboard(ctx context.Context, chatID, topicID int64, text string, buttons []Button) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.record(Call{Method: "SendInlineKeyboard", ChatID: chatID, TopicID: topicID, Text: text, Buttons: append([]Button(nil), buttons...)})
	if err := m.maybeFail("SendInlineKeyboard", chatID); err != nil {
		return 0, err
	}
	return id, nil
}

// flattenGrid joins a grid of buttons into one slice so the mock's existing
// Buttons field can record them. Row breaks are not preserved in the mock;
// tests that need to assert layout should query the underlying call manually.
func flattenGrid(rows [][]Button) []Button {
	out := []Button{}
	for _, r := range rows {
		out = append(out, r...)
	}
	return out
}

func (m *Mock) SendKeyboardGrid(ctx context.Context, chatID, topicID int64, text string, rows [][]Button) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.record(Call{Method: "SendKeyboardGrid", ChatID: chatID, TopicID: topicID, Text: text, Buttons: flattenGrid(rows)})
	if err := m.maybeFail("SendKeyboardGrid", chatID); err != nil {
		return 0, err
	}
	return id, nil
}

func (m *Mock) EditKeyboardGrid(ctx context.Context, chatID, messageID int64, text string, rows [][]Button) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "EditKeyboardGrid", ChatID: chatID, MessageID: messageID, Text: text, Buttons: flattenGrid(rows)})
	return m.maybeFail("EditKeyboardGrid", chatID)
}

func (m *Mock) EditMessage(ctx context.Context, chatID, messageID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "EditMessage", ChatID: chatID, MessageID: messageID, Text: text})
	return m.maybeFail("EditMessage", chatID)
}

func (m *Mock) EditKeyboard(ctx context.Context, chatID, messageID int64, text string, buttons []Button) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "EditKeyboard", ChatID: chatID, MessageID: messageID, Text: text, Buttons: append([]Button(nil), buttons...)})
	return m.maybeFail("EditKeyboard", chatID)
}

func (m *Mock) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "DeleteMessage", ChatID: chatID, MessageID: messageID})
	return m.maybeFail("DeleteMessage", chatID)
}

func (m *Mock) PinMessage(ctx context.Context, chatID, messageID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "PinMessage", ChatID: chatID, MessageID: messageID})
	return m.maybeFail("PinMessage", chatID)
}

func (m *Mock) AnswerCallback(ctx context.Context, callbackQueryID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "AnswerCallback", Callback: callbackQueryID, Text: text})
	return m.maybeFail("AnswerCallback", 0)
}

func (m *Mock) SendReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "SendReaction", ChatID: chatID, MessageID: messageID, Emoji: emoji})
	return m.maybeFail("SendReaction", chatID)
}

func (m *Mock) LeaveChat(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "LeaveChat", ChatID: chatID})
	return m.maybeFail("LeaveChat", chatID)
}

// IsChatAdmin returns the value previously configured via SetChatAdmin (or
// false if not set). Always records the call.
func (m *Mock) IsChatAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "IsChatAdmin", ChatID: chatID, MessageID: userID})
	if err := m.maybeFail("IsChatAdmin", chatID); err != nil {
		return false, err
	}
	if m.chatAdmins == nil {
		return false, nil
	}
	return m.chatAdmins[chatAdminKey{chatID, userID}], nil
}

// SetChatAdmin marks (chatID, userID) as admin in the mock.
func (m *Mock) SetChatAdmin(chatID, userID int64, isAdmin bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chatAdmins == nil {
		m.chatAdmins = map[chatAdminKey]bool{}
	}
	m.chatAdmins[chatAdminKey{chatID, userID}] = isAdmin
}

type chatAdminKey struct{ Chat, User int64 }

// ResolveChatMemberUsername returns the value previously configured via
// SetChatMemberUsername (or "" if not set). Records the call.
func (m *Mock) ResolveChatMemberUsername(ctx context.Context, chatID, userID int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Method: "ResolveChatMemberUsername", ChatID: chatID, MessageID: userID})
	if err := m.maybeFail("ResolveChatMemberUsername", chatID); err != nil {
		return "", err
	}
	if m.chatMemberUsernames == nil {
		return "", nil
	}
	return m.chatMemberUsernames[chatAdminKey{chatID, userID}], nil
}

// SetChatMemberUsername seeds the value ResolveChatMemberUsername will return
// for the given (chatID, userID).
func (m *Mock) SetChatMemberUsername(chatID, userID int64, username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chatMemberUsernames == nil {
		m.chatMemberUsernames = map[chatAdminKey]string{}
	}
	m.chatMemberUsernames[chatAdminKey{chatID, userID}] = username
}

// Pretty returns a human-readable rendering of all calls, useful in test
// failure messages.
func (m *Mock) Pretty() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := ""
	for i, c := range m.calls {
		out += fmt.Sprintf("%d. %s chat=%d topic=%d msg=%d text=%q\n", i+1, c.Method, c.ChatID, c.TopicID, c.MessageID, c.Text)
	}
	return out
}
