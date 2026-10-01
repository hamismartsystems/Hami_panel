package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A small Telegram client. Long polling and four methods is the whole
// surface this bot needs, and a dependency for that would be a liability
// in a binary that also terminates customer traffic.

type api struct {
	token  string
	client *http.Client
}

func newAPI(token string) *api {
	return &api{token: token, client: &http.Client{Timeout: 70 * time.Second}}
}

func (a *api) call(ctx context.Context, method string, params url.Values, out any) error {
	endpoint := "https://api.telegram.org/bot" + a.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: telegram sent something that is not json: %s",
			method, truncate(string(body), 160))
	}
	if !env.OK {
		return fmt.Errorf("%s: %s", method, env.Description)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// sendPhoto uploads an image, which is how a QR code reaches a customer
// without any third party ever seeing their key.
func (a *api) sendPhoto(ctx context.Context, chat int64, name string,
	image []byte, caption string, markup string,
) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", fmt.Sprint(chat))
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	if markup != "" {
		_ = mw.WriteField("reply_markup", markup)
	}
	part, err := mw.CreateFormFile("photo", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(image); err != nil {
		return err
	}
	mw.Close()

	endpoint := "https://api.telegram.org/bot" + a.token + "/sendPhoto"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &env)
	if !env.OK {
		return fmt.Errorf("sendPhoto: %s", env.Description)
	}
	return nil
}

/* ── the shapes we actually read ──────────────────────────────────── */

type Update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		From      *User `json:"from"`
		Chat      *struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text  string `json:"text"`
		Photo []struct {
			FileID string `json:"file_id"`
		} `json:"photo"`
		Caption string `json:"caption"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		From    *User  `json:"from"`
		Data    string `json:"data"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      *struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

func (a *api) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	v := url.Values{}
	v.Set("offset", fmt.Sprint(offset))
	v.Set("timeout", fmt.Sprint(timeout))
	v.Set("allowed_updates", `["message","callback_query"]`)
	var out []Update
	err := a.call(ctx, "getUpdates", v, &out)
	return out, err
}

func (a *api) send(ctx context.Context, chat int64, text, markup string) error {
	v := url.Values{}
	v.Set("chat_id", fmt.Sprint(chat))
	v.Set("text", text)
	v.Set("disable_web_page_preview", "true")
	if markup != "" {
		v.Set("reply_markup", markup)
	}
	return a.call(ctx, "sendMessage", v, nil)
}

func (a *api) answer(ctx context.Context, id, text string) error {
	v := url.Values{}
	v.Set("callback_query_id", id)
	if text != "" {
		v.Set("text", text)
	}
	return a.call(ctx, "answerCallbackQuery", v, nil)
}

// forwardPhoto hands the operator the receipt exactly as it arrived.
func (a *api) forwardPhoto(ctx context.Context, to int64, fileID, caption, markup string) error {
	v := url.Values{}
	v.Set("chat_id", fmt.Sprint(to))
	v.Set("photo", fileID)
	if caption != "" {
		v.Set("caption", caption)
	}
	if markup != "" {
		v.Set("reply_markup", markup)
	}
	return a.call(ctx, "sendPhoto", v, nil)
}

func (a *api) me(ctx context.Context) (*User, error) {
	var u User
	if err := a.call(ctx, "getMe", url.Values{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

/* ── keyboards ────────────────────────────────────────────────────── */

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

func keyboard(rows ...[]button) string {
	b, _ := json.Marshal(map[string]any{"inline_keyboard": rows})
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
