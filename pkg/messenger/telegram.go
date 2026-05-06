package messenger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// telegramAPI implements Messenger by speaking the Telegram Bot HTTP API.
type telegramAPI struct {
	token  string
	client *http.Client
}

// NewTelegram returns a Messenger that talks to the Telegram Bot API.
func NewTelegram(token string) Messenger {
	return &telegramAPI{
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

const tgEndpoint = "https://api.telegram.org/bot"

type tgResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Result      T      `json:"result,omitempty"`
}

func (t *telegramAPI) call(ctx context.Context, method string, payload map[string]any, into any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tgEndpoint+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	wrapper := struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description,omitempty"`
		ErrorCode   int             `json:"error_code,omitempty"`
		Result      json.RawMessage `json:"result,omitempty"`
	}{}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("%s: decode response: %w; body=%s", method, err, string(raw))
	}
	if !wrapper.OK {
		switch wrapper.ErrorCode {
		case 403:
			return fmt.Errorf("%s: %w (%s)", method, ErrForbidden, wrapper.Description)
		case 400, 404:
			if isNotFound(wrapper.Description) {
				return fmt.Errorf("%s: %w (%s)", method, ErrNotFound, wrapper.Description)
			}
			fallthrough
		default:
			return fmt.Errorf("%s: telegram %d: %s", method, wrapper.ErrorCode, wrapper.Description)
		}
	}
	if into != nil && len(wrapper.Result) > 0 {
		if err := json.Unmarshal(wrapper.Result, into); err != nil {
			return fmt.Errorf("%s: decode result: %w", method, err)
		}
	}
	return nil
}

func isNotFound(desc string) bool {
	for _, s := range []string{"chat not found", "message to edit not found", "MESSAGE_ID_INVALID"} {
		if contains(desc, s) {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || index(haystack, needle) >= 0)
}

// index is a tiny strings.Index reimplementation to avoid an import cycle.
func index(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (t *telegramAPI) SendMessage(ctx context.Context, chatID, topicID int64, text string) (int64, error) {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if topicID > 0 {
		payload["message_thread_id"] = topicID
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	if err := t.call(ctx, "sendMessage", payload, &msg); err != nil {
		return 0, err
	}
	return msg.MessageID, nil
}

func (t *telegramAPI) SendKeyboard(ctx context.Context, chatID, topicID int64, text, leftLabel, leftCallback, rightLabel, rightCallback string) (int64, error) {
	kb := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": leftLabel, "callback_data": leftCallback},
				{"text": rightLabel, "callback_data": rightCallback},
			},
		},
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "reply_markup": kb}
	if topicID > 0 {
		payload["message_thread_id"] = topicID
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	if err := t.call(ctx, "sendMessage", payload, &msg); err != nil {
		return 0, err
	}
	return msg.MessageID, nil
}

func (t *telegramAPI) EditMessage(ctx context.Context, chatID, messageID int64, text string) error {
	return t.call(ctx, "editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}, nil)
}

func (t *telegramAPI) EditKeyboard(ctx context.Context, chatID, messageID int64, text string, buttons []Button) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if len(buttons) > 0 {
		row := make([]map[string]string, 0, len(buttons))
		for _, b := range buttons {
			row = append(row, map[string]string{"text": b.Label, "callback_data": b.Callback})
		}
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{row}}
	} else {
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{}}
	}
	return t.call(ctx, "editMessageText", payload, nil)
}

func (t *telegramAPI) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return t.call(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
}

func (t *telegramAPI) PinMessage(ctx context.Context, chatID, messageID int64) error {
	return t.call(ctx, "pinChatMessage", map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"disable_notification": true,
	}, nil)
}

func (t *telegramAPI) AnswerCallback(ctx context.Context, callbackQueryID, text string) error {
	payload := map[string]any{"callback_query_id": callbackQueryID}
	if text != "" {
		payload["text"] = text
	}
	return t.call(ctx, "answerCallbackQuery", payload, nil)
}

func (t *telegramAPI) SendReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	return t.call(ctx, "setMessageReaction", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}, nil)
}

func (t *telegramAPI) LeaveChat(ctx context.Context, chatID int64) error {
	return t.call(ctx, "leaveChat", map[string]any{"chat_id": chatID}, nil)
}

// Quick formatters used by the command-routing layer.

// FormatChatID turns a chat ID into the form Telegram accepts.
func FormatChatID(id int64) string { return strconv.FormatInt(id, 10) }

// MakeWebhookURL builds the URL Telegram should be told to call for setWebhook.
func MakeWebhookURL(baseGatewayURL string) string {
	u, _ := url.Parse(baseGatewayURL)
	return u.String()
}
