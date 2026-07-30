// Package telegram implements the bot: a remote control and monitor for the
// server, reachable from a phone.
//
// It talks to the Bot API directly over HTTPS rather than pulling in a client
// library — the bot needs four endpoints, and hand-rolling them keeps the
// dependency tree as small as the rest of the project.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

const apiBase = "https://api.telegram.org/bot"

// api is a minimal Bot API client.
type api struct {
	token string
	http  *http.Client
}

func newAPI(token string) *api {
	return &api{
		token: token,
		// Long polling holds a request open for the poll timeout, so the client
		// timeout must exceed it comfortably.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

func (a *api) url(method string) string { return apiBase + a.token + "/" + method }

// call posts a JSON payload and decodes the result into out (which may be nil).
func (a *api) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// --- types ------------------------------------------------------------------

// Update is one incoming event.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// InlineKeyboard is the button grid attached to a message.
type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

type InlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// --- methods ----------------------------------------------------------------

// getMe verifies the token and returns the bot's own identity.
func (a *api) getMe(ctx context.Context) (*User, error) {
	var u User
	if err := a.call(ctx, "getMe", map[string]any{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// getUpdates long-polls for events newer than offset.
func (a *api) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var ups []Update
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": timeout,
		// Only the update kinds this bot acts on, so nothing else is queued up.
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// send posts a message, optionally with a keyboard.
func (a *api) send(ctx context.Context, chatID int64, text string, kb *InlineKeyboard) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	return a.call(ctx, "sendMessage", payload, nil)
}

// edit replaces an existing message in place, which is what makes the button
// grid feel like a live panel rather than a growing transcript.
func (a *api) edit(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboard) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	return a.call(ctx, "editMessageText", payload, nil)
}

// answerCallback clears the loading spinner on a tapped button.
func (a *api) answerCallback(ctx context.Context, id, text string) error {
	return a.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	}, nil)
}

// sendDocument uploads a file, used by the backup command.
func (a *api) sendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", fmt.Sprint(chatID))
	if caption != "" {
		_ = mw.WriteField("caption", caption)
		_ = mw.WriteField("parse_mode", "HTML")
	}
	part, err := mw.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url("sendDocument"), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram sendDocument: %s: %s", resp.Status, b)
	}
	return nil
}

// setCommands registers the slash-command menu shown in Telegram's UI.
func (a *api) setCommands(ctx context.Context, cmds []botCommand) error {
	return a.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// deleteWebhook clears any webhook so long polling is allowed to run.
func (a *api) deleteWebhook(ctx context.Context) error {
	return a.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": true}, nil)
}

var _ = url.Values{} // reserved for future query-style calls
