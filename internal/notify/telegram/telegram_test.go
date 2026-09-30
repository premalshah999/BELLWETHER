package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tradesys/dashboard/internal/alerts"
)

// fakeAPI stands in for Telegram, recording what it received.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []sendRequest
	paths    []string
	status   int
	body     string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: http.StatusOK, body: `{"ok":true,"result":{"message_id":1}}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req sendRequest
		json.Unmarshal(raw, &req)

		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.paths = append(f.paths, r.URL.Path)
		status, body := f.status, f.body
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) last() sendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestSend(t *testing.T) {
	api := newFakeAPI(t)
	c := New("bot-token", "12345", WithBaseURL(api.URL))

	err := c.Send(context.Background(), alerts.Message{
		Title: "[ALGO] Momentum watch — XOM",
		Body:  "RSI(14)=32.10 < 35\nAI: context here",
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.count() != 1 {
		t.Fatalf("sent %d requests, want 1", api.count())
	}

	got := api.last()
	if got.ChatID != "12345" {
		t.Errorf("chat_id = %q", got.ChatID)
	}
	if !strings.HasPrefix(got.Text, "[ALGO] Momentum watch — XOM") {
		t.Errorf("text does not lead with the title:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "RSI(14)=32.10") {
		t.Errorf("text is missing the snapshot:\n%s", got.Text)
	}
	if !got.DisableWebPagePreview {
		t.Error("web page previews should be disabled")
	}

	api.mu.Lock()
	path := api.paths[0]
	api.mu.Unlock()
	if path != "/botbot-token/sendMessage" {
		t.Errorf("path = %q, want the token in the path and the sendMessage method", path)
	}
}

func TestConfigured(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		chatID string
		want   bool
	}{
		{name: "both set", token: "t", chatID: "c", want: true},
		{name: "no token", token: "", chatID: "c", want: false},
		{name: "no chat id", token: "t", chatID: "", want: false},
		{name: "neither", token: "", chatID: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.token, tc.chatID).Configured(); got != tc.want {
				t.Errorf("Configured = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSendUnconfigured(t *testing.T) {
	err := New("", "").Send(context.Background(), alerts.Message{Title: "x"})
	if err == nil {
		t.Error("want an error when sending without credentials")
	}
}

func TestAPIErrorsAreSurfaced(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantText string
	}{
		{
			name:     "telegram rejects the chat",
			status:   http.StatusBadRequest,
			body:     `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`,
			wantText: "chat not found",
		},
		{
			name:     "bot token revoked",
			status:   http.StatusUnauthorized,
			body:     `{"ok":false,"error_code":401,"description":"Unauthorized"}`,
			wantText: "Unauthorized",
		},
		{
			name:     "rate limited",
			status:   http.StatusTooManyRequests,
			body:     `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30"}`,
			wantText: "retry after 30",
		},
		{
			name:     "html error page instead of json",
			status:   http.StatusBadGateway,
			body:     "<html>502 Bad Gateway</html>",
			wantText: "502",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.status, api.body = tc.status, tc.body
			c := New("token", "chat", WithBaseURL(api.URL))

			err := c.Send(context.Background(), alerts.Message{Title: "x"})
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

func TestTokenIsNeverLeakedInErrors(t *testing.T) {
	// Transport errors include the request URL, which contains the bot token.
	// A token in a log line is a credential leak.
	const token = "123456:SUPER-SECRET-TOKEN"
	c := New(token, "chat", WithBaseURL("http://127.0.0.1:1")) // nothing listening

	err := c.Send(context.Background(), alerts.Message{Title: "x"})
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-TOKEN") {
		t.Errorf("the bot token leaked into an error message: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error %q should show the token was redacted", err)
	}
}

func TestRenderIsPlainText(t *testing.T) {
	// No markdown: an indicator label like MACD(12,26,9) or a value with an
	// underscore would otherwise be mangled or rejected as bad entities.
	msg := alerts.Message{
		Title: "[ALGO] Test_Algo — XOM",
		Body:  "MACD(12,26,9)=1.5 > *signal* _underscored_ [bracket](x)",
	}
	got := Render(msg)
	if !strings.Contains(got, "*signal*") || !strings.Contains(got, "_underscored_") {
		t.Errorf("markdown characters were altered:\n%s", got)
	}
}

func TestLongMessagesAreTruncatedNotRejected(t *testing.T) {
	long := strings.Repeat("x", maxMessageBytes*2)
	got := Render(alerts.Message{Title: "t", Body: long})

	if len(got) > maxMessageBytes {
		t.Errorf("rendered %d bytes, over Telegram's %d limit", len(got), maxMessageBytes)
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Error("truncation should be visible to the reader")
	}
}

func TestTruncationRespectsRuneBoundaries(t *testing.T) {
	// Multibyte text must not be cut mid-rune, which would produce invalid
	// UTF-8 that Telegram rejects outright.
	body := strings.Repeat("नमस्ते", 2000)
	got := Render(alerts.Message{Title: "t", Body: body})

	if len(got) > maxMessageBytes {
		t.Errorf("rendered %d bytes, over the limit", len(got))
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("invalid UTF-8 produced at byte %d", i)
		}
	}
}

func TestChannel(t *testing.T) {
	if got := New("t", "c").Channel(); got != "telegram" {
		t.Errorf("Channel = %q, want telegram", got)
	}
}

func TestContextCancellation(t *testing.T) {
	api := newFakeAPI(t)
	c := New("token", "chat", WithBaseURL(api.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Send(ctx, alerts.Message{Title: "x"}); err == nil {
		t.Error("want an error on a cancelled context")
	}
}
