// Package algo defines the algorithm rule language: its JSON shape, its
// validation rules, and its evaluator.
//
// This is the most safety-critical code in the application. An algorithm that
// silently misreads its own rules will notify the operators about something
// that did not happen, and they will act on it. Three principles follow from
// that:
//
//   - Missing data is never zero. An indicator with no value yet makes a
//     condition *unknown*, not false, and an unknown result never fires an
//     alert. See the three-valued logic in eval.go.
//   - Validation is total and happens before storage. A rule set that cannot
//     be evaluated must be rejected at the point the operator writes it, with
//     a message naming the exact field, not at 09:15 the next morning.
//   - Every evaluation records what it saw. Each alert stores the full operand
//     snapshot so a surprising notification can be audited afterwards.
package algo

import (
	"bytes"
	"encoding/json"
	"time"
)

// Algorithm is a stored rule set that watches a set of symbols.
type Algorithm struct {
	ID      int64    `json:"id,omitempty"`
	Name    string   `json:"name"`
	Symbols []string `json:"symbols"`
	// WatchlistIDs point this rule at named lists instead of, or as well as,
	// fixed symbols. The evaluated set is the union of the two, resolved at
	// run time — so adding an instrument to a list brings every rule watching
	// that list along with it, rather than requiring each to be edited.
	WatchlistIDs []int64 `json:"watchlist_ids,omitempty"`
	Interval     string  `json:"interval"`

	// All conditions must hold; Any requires at least one. Exactly one of the
	// two must be present at the top level.
	All []Node `json:"all,omitempty"`
	Any []Node `json:"any,omitempty"`

	// CooldownHours suppresses repeat alerts for the same symbol. Zero means
	// no suppression, which is almost never what an operator wants and is
	// warned about rather than rejected.
	CooldownHours float64 `json:"cooldown_hours"`

	Notify  NotifyConfig `json:"notify"`
	Enabled bool         `json:"enabled"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// NotifyConfig selects the delivery channels for an algorithm's alerts.
type NotifyConfig struct {
	Telegram bool `json:"telegram"`
	// AIContext asks the AI layer for a short brief to attach to the alert.
	// When the AI layer is unconfigured or over budget the alert still fires,
	// carrying an explicit "AI context unavailable" note.
	AIContext bool `json:"ai_context"`
}

// Node is one element of a condition group. It is either a nested group — with
// All or Any set — or a leaf condition. Never both; Validate enforces it.
//
// The two forms share a struct because that is the JSON shape the rule
// language uses, and because a tagged union would make the hand-written JSON
// an operator types considerably more verbose.
type Node struct {
	// Group form. Only one level of nesting is permitted.
	All []Node `json:"all,omitempty"`
	Any []Node `json:"any,omitempty"`

	// Leaf form: the left-hand operand is flattened into the node.
	Indicator string `json:"indicator,omitempty"`
	Period    int    `json:"period,omitempty"`
	Fast      int    `json:"fast,omitempty"`
	Slow      int    `json:"slow,omitempty"`
	Signal    int    `json:"signal,omitempty"`

	// Shift reads the operand this many bars back. Zero is the current bar.
	Shift int `json:"shift,omitempty"`

	Op string `json:"op,omitempty"`

	// Exactly one of Value or Compare supplies the right-hand side.
	Value   *float64 `json:"value,omitempty"`
	Compare *Operand `json:"compare,omitempty"`
}

// IsGroup reports whether this node is a nested condition group.
func (n Node) IsGroup() bool { return len(n.All) > 0 || len(n.Any) > 0 }

// left builds the operand described by the node's flattened left-hand fields.
func (n Node) left() Operand {
	return Operand{
		Indicator: n.Indicator,
		Period:    n.Period,
		Fast:      n.Fast,
		Slow:      n.Slow,
		Signal:    n.Signal,
		Shift:     n.Shift,
	}
}

// Operand is one side of a comparison: an indicator, optionally scaled.
type Operand struct {
	Indicator string `json:"indicator"`
	Period    int    `json:"period,omitempty"`
	Fast      int    `json:"fast,omitempty"`
	Slow      int    `json:"slow,omitempty"`
	Signal    int    `json:"signal,omitempty"`

	// Mult scales the computed value, which is how "volume above 1.5x its
	// average" is expressed. Zero means 1 — an unset multiplier must not
	// annihilate the operand.
	Mult float64 `json:"mult,omitempty"`
	// Offset is added after scaling.
	Offset float64 `json:"offset,omitempty"`

	// Shift reads the value this many bars before the one being evaluated.
	//
	// It exists because some questions are only meaningful against history.
	// "Is the close above the 52-week high" is nonsense on the current bar —
	// that high includes today, and a close can never exceed its own bar's
	// high. What a breakout rule actually asks is whether today's close
	// exceeds the 52-week high *as it stood yesterday*, which is Shift: 1.
	Shift int `json:"shift,omitempty"`
}

// multiplier returns the effective scale factor.
func (o Operand) multiplier() float64 {
	if o.Mult == 0 {
		return 1
	}
	return o.Mult
}

// Parse decodes an algorithm from JSON and validates it.
//
// Unknown fields are rejected: a typo such as "cooldown_hour" would otherwise
// be silently discarded and the operator would get alert spam they explicitly
// tried to prevent.
func Parse(data []byte) (*Algorithm, error) {
	reader := bytes.NewReader(bytes.TrimSpace(data))
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()

	var a Algorithm
	if err := dec.Decode(&a); err != nil {
		return nil, &ValidationError{Field: "", Message: friendlyJSONError(err)}
	}
	// Anything after the object — valid JSON or not — means the operator
	// pasted something they did not intend, so refuse rather than silently
	// honouring only the first half.
	var tail bytes.Buffer
	tail.ReadFrom(dec.Buffered())
	tail.ReadFrom(reader)
	if len(bytes.TrimSpace(tail.Bytes())) > 0 {
		return nil, &ValidationError{Field: "", Message: "unexpected content after the JSON object"}
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &a, nil
}

// MarshalJSON is the canonical encoding used for storage and for the UI's live
// preview.
func (a *Algorithm) MarshalIndent() ([]byte, error) {
	return json.MarshalIndent(a, "", "  ")
}
