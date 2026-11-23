// Package tg is a minimal Telegram Bot API client: long polling, text messages, documents.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Message is the part of an incoming message the agent uses.
type Message struct {
	ChatID int64
	From   string
	Text   string
}

// Client talks to api.telegram.org.
type Client struct {
	Token string
	HTTP  *http.Client // must have a timeout longer than the long-poll timeout
	Log   func(format string, args ...any)

	offset int64
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// ErrRetryAfter carries Telegram's flood-control wait.
type ErrRetryAfter struct{ Wait time.Duration }

func (e ErrRetryAfter) Error() string { return fmt.Sprintf("telegram: retry after %s", e.Wait) }

func (c *Client) url(method string) string {
	return "https://api.telegram.org/bot" + c.Token + "/" + method
}

func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// never log the URL: it contains the token
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("%s: %w", ue.Op, ue.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var r apiResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("telegram: http %d, bad json", resp.StatusCode)
	}
	if !r.OK {
		if r.Parameters.RetryAfter > 0 {
			return nil, ErrRetryAfter{time.Duration(r.Parameters.RetryAfter) * time.Second}
		}
		return nil, fmt.Errorf("telegram: %d %s", resp.StatusCode, r.Description)
	}
	return r.Result, nil
}

func (c *Client) postForm(ctx context.Context, method string, v url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.url(method), bytes.NewBufferString(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

// GetUpdates long-polls for new messages (timeout seconds).
func (c *Client) GetUpdates(ctx context.Context, timeout int) ([]Message, error) {
	v := url.Values{}
	v.Set("timeout", strconv.Itoa(timeout))
	v.Set("offset", strconv.FormatInt(c.offset, 10))
	v.Set("allowed_updates", `["message"]`)
	res, err := c.postForm(ctx, "getUpdates", v)
	if err != nil {
		return nil, err
	}
	var ups []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			From struct {
				Username  string `json:"username"`
				FirstName string `json:"first_name"`
			} `json:"from"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(res, &ups); err != nil {
		return nil, err
	}
	var out []Message
	for _, u := range ups {
		if u.UpdateID >= c.offset {
			c.offset = u.UpdateID + 1
		}
		if u.Message != nil {
			from := u.Message.From.Username
			if from == "" {
				from = u.Message.From.FirstName
			}
			out = append(out, Message{ChatID: u.Message.Chat.ID, From: from, Text: u.Message.Text})
		}
	}
	return out, nil
}

// Send sends a plain-text message.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("text", text)
	v.Set("disable_web_page_preview", "true")
	_, err := c.postForm(ctx, "sendMessage", v)
	return err
}

// SendDocument uploads a file.
func (c *Client) SendDocument(ctx context.Context, chatID int64, path, caption string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		mw.WriteField("caption", caption)
	}
	fw, err := mw.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", c.url("sendDocument"), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	_, err = c.do(req)
	return err
}

// Outbox delivers messages to one chat in order, retrying until sent (bounded queue).
type Outbox struct {
	c  *Client
	ch chan string
}

// NewOutbox starts the sender goroutine. chatID returns the current owner (0 = drop).
func NewOutbox(ctx context.Context, c *Client, chatID func() int64, size int) *Outbox {
	o := &Outbox{c: c, ch: make(chan string, size)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case text := <-o.ch:
				for wait := 5 * time.Second; ; {
					id := chatID()
					if id == 0 {
						break
					}
					sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
					err := c.Send(sctx, id, text)
					cancel()
					if err == nil {
						break
					}
					var ra ErrRetryAfter
					if errors.As(err, &ra) {
						wait = ra.Wait
					}
					if c.Log != nil {
						c.Log("telegram send failed, retry in %s: %v", wait, err)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
					if wait < 5*time.Minute {
						wait *= 2
					}
				}
			}
		}
	}()
	return o
}

// Post queues a message; if the queue is full the oldest-first order is kept and the new one is dropped.
func (o *Outbox) Post(text string) bool {
	select {
	case o.ch <- text:
		return true
	default:
		return false
	}
}
