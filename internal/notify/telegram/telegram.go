// Package telegram delivers alerts through the Telegram Bot API.
//
// Only sending is implemented. Long polling exists to *receive* commands,
// which this version has no use for: the dashboard notifies, it does not take
// instructions from chat.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/alerts"
)

// DefaultBaseURL is Telegram's API host. Overridden in tests.
const DefaultBaseURL = "https://api.telegram.org"

// Channel is the identifier this notifier records on delivery records.
const Channel = "telegram"

// maxMessageBytes is Telegram's hard limit on a message. Exceeding it is a
// 400, so long alerts are truncated rather than lost.
const maxMessageBytes = 4096

// Client sends messages to one chat.
type Client struct {
	baseURL string
	token   string
	chatID  string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a different host, used by tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Telegram notifier. Empty credentials produce an unconfigured
// notifier, which the pipeline skips rather than treating as a failure.
func New(token, chatID string, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		token:   token,
		chatID:  chatID,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Channel identifies this delivery route.
func (c *Client) Channel() string { return Channel }

// Configured reports whether both a bot token and a chat id were supplied.
func (c *Client) Configured() bool { return c.token != "" && c.chatID != "" }

type sendRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type sendResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// Send delivers one message.
func (c *Client) Send(ctx context.Context, msg alerts.Message) error {
	if !c.Configured() {
		return fmt.Errorf("telegram: not configured")
	}

	text := Render(msg)
	body, err := json.Marshal(sendRequest{
		ChatID: c.chatID,
		Text:   text,
		// Alert bodies contain no links worth previewing, and a preview card
		// would push the numbers off a phone screen.
		DisableWebPagePreview: true,
	})
	if err != nil {
		return fmt.Errorf("telegram: encode request: %w", err)
	}

	// The token is a credential and must never appear in a log line, so it is
	// only ever interpolated here.
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, url.PathEscape(c.token))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send: %w", redact(err, c.token))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("telegram: read response: %w", err)
	}

	var out sendResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("telegram: http %d with an unreadable body", resp.StatusCode)
	}
	if !out.OK {
		return fmt.Errorf("telegram: rejected (%d): %s", out.ErrorCode, out.Description)
	}
	return nil
}

// Render formats a message for Telegram.
//
// Plain text, no markdown: an indicator label such as "MACD(12,26,9)" or a
// value containing an underscore would otherwise be mangled by Telegram's
// parser, or worse, rejected as malformed entities.
func Render(msg alerts.Message) string {
	var b strings.Builder
	b.WriteString(msg.Title)
	if msg.Body != "" {
		b.WriteString("\n")
		b.WriteString(msg.Body)
	}
	return truncate(b.String(), maxMessageBytes)
}

// truncate shortens text to fit Telegram's limit without splitting a rune.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ellipsis = "\n[truncated]"
	cut := limit - len(ellipsis)
	if cut < 0 {
		cut = 0
	}
	// Step back to a rune boundary.
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// redact removes the bot token from an error string, since transport errors
// include the request URL.
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "[redacted]")
	msg = strings.ReplaceAll(msg, url.PathEscape(token), "[redacted]")
	return fmt.Errorf("%s", msg)
}

var _ alerts.Notifier = (*Client)(nil)
