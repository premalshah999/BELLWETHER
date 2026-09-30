package research

import (
	"context"
	"time"
)

// Conversation is a research thread.
type Conversation struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	TurnCount int       `json:"turn_count"`
	Symbols   []string  `json:"symbols,omitempty"`
	Archived  bool      `json:"archived"`
	Turns     []Turn    `json:"turns,omitempty"`
}

// Section is one part of a research report.
type Section struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
	Sources []int  `json:"sources,omitempty"`
}

// Claim is one cited assertion in an answer.
type Claim struct {
	Claim      string `json:"claim"`
	Sources    []int  `json:"sources"`
	Confidence string `json:"confidence,omitempty"`
}

// CompanyRef is one instrument an answer concerns.
type CompanyRef struct {
	Symbol    string `json:"symbol"`
	Relevance string `json:"relevance,omitempty"`
	Direction string `json:"direction,omitempty"`
	Sources   []int  `json:"sources,omitempty"`
}

// ProviderReport is what one search provider contributed to a turn.
type ProviderReport struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// Status is where a turn's work has got to.
type Status string

const (
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Step is one line of the running commentary.
//
// Recorded rather than streamed, so the progress survives a page reload and a
// tab switch. A spinner tells the reader nothing; "reuters 0 · mint 6 ·
// synthesising from 25 documents" tells them what is happening and, when it is
// slow, which provider is being slow.
type Step struct {
	At     time.Time `json:"at"`
	Stage  string    `json:"stage"`
	Detail string    `json:"detail,omitempty"`
}

// Turn is one question and its answer.
type Turn struct {
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	Seq            int    `json:"seq"`
	Question       string `json:"question"`
	// SearchQuery is what was actually sent to the providers. For a follow-up
	// it is rewritten from the question plus the thread, and it is surfaced
	// because a disappointing result is far more often a bad rewrite than a
	// bad search — and without showing it, that is invisible.
	SearchQuery string `json:"search_query,omitempty"`

	Answer string `json:"answer"`
	// Sections are the body of the report. The summary says what was found;
	// these say it at length, in the order that matters.
	Sections  []Section    `json:"sections,omitempty"`
	Findings  []Claim      `json:"findings,omitempty"`
	Companies []CompanyRef `json:"companies,omitempty"`
	Gaps      []string     `json:"gaps,omitempty"`
	Followups []string     `json:"followups,omitempty"`
	Sources   []Finding    `json:"sources,omitempty"`
	// Measurements are computed from the price series rather than retrieved.
	// Independent of the model, so they survive a failed synthesis.
	Measurements []MarketStats    `json:"measurements,omitempty"`
	Analyses     []Analysis       `json:"analyses,omitempty"`
	Providers    []ProviderReport `json:"providers,omitempty"`

	Model     string    `json:"model,omitempty"`
	Degraded  bool      `json:"degraded"`
	Note      string    `json:"note,omitempty"`
	ElapsedMS int       `json:"elapsed_ms"`
	CreatedAt time.Time `json:"created_at"`

	Status     Status    `json:"status"`
	Stage      string    `json:"stage,omitempty"`
	Progress   []Step    `json:"progress,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Running reports whether this turn is still being worked on.
func (t Turn) Running() bool { return t.Status == StatusRunning }

// Store persists conversations. Declared at the point of use.
type Store interface {
	CreateConversation(ctx context.Context, title string) (int64, error)
	ListConversations(ctx context.Context, limit int, includeArchived bool) ([]Conversation, error)
	GetConversation(ctx context.Context, id int64) (Conversation, error)
	// StartTurn records a question and returns immediately, before any work
	// is done. The turn exists in 'running' from that moment, which is what
	// lets the client leave and come back.
	StartTurn(ctx context.Context, conversationID int64, question string) (*Turn, error)
	// RecordProgress appends one line of commentary.
	RecordProgress(ctx context.Context, turnID int64, stage, detail string) error
	// CompleteTurn stores the finished answer.
	CompleteTurn(ctx context.Context, t *Turn) error
	// FailTurn records that the work could not be completed.
	FailTurn(ctx context.Context, turnID int64, reason string) error
	// ReapAbandonedTurns fails turns left running by a process that died.
	ReapAbandonedTurns(ctx context.Context, olderThan time.Duration) (int, error)
	ArchiveConversation(ctx context.Context, id int64, archived bool) error
	DeleteConversation(ctx context.Context, id int64) error
}

// History renders the thread so far as context for a follow-up.
//
// Only questions and answers are included, never the source lists. A thread of
// five turns carries perhaps a hundred retrieved documents, and replaying them
// into every follow-up would cost more than the search itself while adding
// little: what the follow-up needs to know is what has already been
// established, not the raw material it was established from.
func (c Conversation) History(maxTurns int) []Turn {
	if maxTurns <= 0 || len(c.Turns) <= maxTurns {
		return c.Turns
	}
	// The most recent turns are the ones a follow-up refers to.
	return c.Turns[len(c.Turns)-maxTurns:]
}

// SymbolsMentioned collects every instrument referenced across the thread.
func (c Conversation) SymbolsMentioned() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range c.Turns {
		for _, co := range t.Companies {
			if co.Symbol != "" && !seen[co.Symbol] {
				seen[co.Symbol] = true
				out = append(out, co.Symbol)
			}
		}
		for _, s := range t.Sources {
			for _, sym := range s.Symbols {
				if !seen[sym] {
					seen[sym] = true
					out = append(out, sym)
				}
			}
		}
	}
	return out
}
