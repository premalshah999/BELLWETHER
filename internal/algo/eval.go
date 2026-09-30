package algo

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/indicators"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// Tri is a three-valued truth value.
//
// The third value is what keeps this system honest. An indicator that has no
// value yet — a 200-day average on 60 bars of history, a gap in the feed —
// makes its condition Unknown, and Unknown never fires an alert. Collapsing it
// to false would be defensible; collapsing it to true would be dangerous; but
// either way the operator would lose the distinction between "the rule did not
// match" and "we could not tell", and those call for different actions.
type Tri int

const (
	// TriUnknown means the condition could not be decided.
	TriUnknown Tri = iota
	// TriFalse means the condition definitely does not hold.
	TriFalse
	// TriTrue means the condition definitely holds.
	TriTrue
)

func (t Tri) String() string {
	switch t {
	case TriTrue:
		return "true"
	case TriFalse:
		return "false"
	default:
		return "unknown"
	}
}

// and combines truth values under Kleene logic: one definite false settles the
// result regardless of any unknowns beside it.
func and(values []Tri) Tri {
	result := TriTrue
	for _, v := range values {
		switch v {
		case TriFalse:
			return TriFalse
		case TriUnknown:
			result = TriUnknown
		}
	}
	return result
}

// or combines truth values under Kleene logic: one definite true settles the
// result. This matters — an "any" group with one satisfied branch should fire
// even if a sibling branch lacks data.
func or(values []Tri) Tri {
	result := TriFalse
	for _, v := range values {
		switch v {
		case TriTrue:
			return TriTrue
		case TriUnknown:
			result = TriUnknown
		}
	}
	return result
}

// Status describes the outcome of evaluating an algorithm against one symbol.
type Status string

const (
	// StatusTriggered means every required condition held.
	StatusTriggered Status = "triggered"
	// StatusNotMet means the conditions were decidable and did not hold.
	StatusNotMet Status = "not_met"
	// StatusInsufficientData means the outcome could not be determined,
	// usually because an indicator needs more history than we have.
	StatusInsufficientData Status = "insufficient_data"
	// StatusError means evaluation could not run at all.
	StatusError Status = "error"
)

// ConditionResult records one leaf comparison, including the numbers it saw.
// Every alert stores these so a surprising notification can be audited later.
type ConditionResult struct {
	Label      string  `json:"label"`
	Op         string  `json:"op"`
	LeftValue  float64 `json:"left_value"`
	RightLabel string  `json:"right_label"`
	RightValue float64 `json:"right_value"`
	Result     string  `json:"result"`
	// Detail explains an unknown result, e.g. "SMA(200) needs 200 bars, have 60".
	Detail string `json:"detail,omitempty"`
	// Text is the human-readable fragment used in alert messages.
	Text string `json:"text"`
}

// Result is one algorithm evaluated against one symbol at one bar.
type Result struct {
	Symbol     string            `json:"symbol"`
	Interval   string            `json:"interval"`
	Status     Status            `json:"status"`
	BarTime    time.Time         `json:"bar_time"`
	Price      float64           `json:"price"`
	Conditions []ConditionResult `json:"conditions"`
	// Summary is the plain-words explanation of what the evaluator saw,
	// suitable for the first line of a notification.
	Summary string `json:"summary"`
	// Reason explains a non-triggered outcome.
	Reason string `json:"reason,omitempty"`
}

// Triggered reports whether this evaluation should raise an alert.
func (r Result) Triggered() bool { return r.Status == StatusTriggered }

// Evaluator evaluates algorithms. It holds no mutable state and is safe for
// concurrent use.
type Evaluator struct {
	// loc was once the timezone every session-based indicator bucketed by,
	// which was wrong the moment a second venue existed: a US symbol's
	// SessionVWAP reset by IST resets mid-session, at 02:30 ET. Evaluate now
	// derives the location per call from the symbol's own venue
	// (sym.Exchange.Location()) instead, so this field is kept only for
	// source compatibility with existing callers and is otherwise unused.
	loc *time.Location
}

// NewEvaluator builds an evaluator. loc is accepted for compatibility with
// existing callers but no longer used: session-based indicators bucket by
// the symbol being evaluated, not a single shared timezone. Pass time.UTC or
// nil.
func NewEvaluator(loc *time.Location) *Evaluator {
	if loc == nil {
		loc = time.UTC
	}
	return &Evaluator{loc: loc}
}

// RequiredBars reports how much history this algorithm needs before any of its
// conditions can be decided, so the scheduler can fetch enough candles in one
// request instead of discovering the shortfall afterwards.
func (a *Algorithm) RequiredBars() int {
	need := 1
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			if n.IsGroup() {
				walk(n.All)
				walk(n.Any)
				continue
			}
			for _, o := range []Operand{n.left(), derefOperand(n.Compare)} {
				if o.Indicator == "" {
					continue
				}
				if def, ok := registry[o.Indicator]; ok {
					if b := def.MinBars(o) + o.Shift; b > need {
						need = b
					}
				}
			}
		}
	}
	walk(a.All)
	walk(a.Any)

	// One extra bar so crossover operators can look at the previous bar, plus
	// headroom so an indicator is not evaluated at the exact first bar it
	// becomes defined.
	return need + 2
}

func derefOperand(o *Operand) Operand {
	if o == nil {
		return Operand{}
	}
	return *o
}

// evalContext memoises indicator series for one evaluation, so an algorithm
// referencing SMA(200) three times computes it once.
type evalContext struct {
	candles []marketdata.Candle
	loc     *time.Location
	cache   map[string]indicators.Series
	// index is the bar being evaluated: the most recent one.
	index int
}

// operandKey identifies a computed series. The shift is deliberately excluded:
// shifting reads a different index of the same series, so two operands that
// differ only by shift share one computation.
func operandKey(o Operand) string {
	return fmt.Sprintf("%s|%d|%d|%d|%d", o.Indicator, o.Period, o.Fast, o.Slow, o.Signal)
}

func (c *evalContext) series(o Operand) (indicators.Series, bool) {
	def, ok := registry[o.Indicator]
	if !ok {
		return nil, false
	}
	key := operandKey(o)
	if s, hit := c.cache[key]; hit {
		return s, true
	}
	s := def.Compute(c.candles, o, c.loc)
	c.cache[key] = s
	return s, true
}

// valueAt returns an operand's value at a bar, with scaling applied.
func (c *evalContext) valueAt(o Operand, index int) (float64, string, bool) {
	def, ok := registry[o.Indicator]
	if !ok {
		return 0, fmt.Sprintf("unknown indicator %q", o.Indicator), false
	}
	label := def.Describe(o)

	at := index - o.Shift
	if at < 0 {
		return 0, fmt.Sprintf("%s looks %d bars back, but only %d bars are available", label, o.Shift, len(c.candles)), false
	}

	s, _ := c.series(o)
	raw := s.At(at)
	if !indicators.IsDefined(raw) {
		need := def.MinBars(o) + o.Shift
		return 0, fmt.Sprintf("%s needs %d bars, have %d", label, need, len(c.candles)), false
	}

	v := raw*o.multiplier() + o.Offset
	if !indicators.IsDefined(v) {
		return 0, label + " produced a non-finite value", false
	}
	return v, "", true
}

// Evaluate runs an algorithm against one symbol's candles.
//
// Candles must be oldest-first; the most recent bar is the one evaluated.
func (e *Evaluator) Evaluate(a *Algorithm, sym marketdata.Symbol, candles []marketdata.Candle) Result {
	res := Result{Symbol: sym.String(), Interval: a.Interval}

	if len(candles) == 0 {
		res.Status = StatusInsufficientData
		res.Reason = "no price data available"
		return res
	}

	ctx := &evalContext{
		candles: candles,
		// The symbol's own exchange decides the session boundary, not a
		// single operator-display timezone: e.loc would bucket a US
		// session's SessionVWAP by IST, resetting it mid-session at 02:30
		// ET.
		loc:   sym.Exchange.Location(),
		cache: map[string]indicators.Series{},
		index: len(candles) - 1,
	}
	last := candles[ctx.index]
	res.BarTime = last.Time
	res.Price = last.Close

	var truth Tri
	if len(a.Any) > 0 {
		truth = e.evalGroup(ctx, a.Any, false, &res.Conditions)
	} else {
		truth = e.evalGroup(ctx, a.All, true, &res.Conditions)
	}

	switch truth {
	case TriTrue:
		res.Status = StatusTriggered
	case TriFalse:
		res.Status = StatusNotMet
		res.Reason = "conditions not met"
	default:
		res.Status = StatusInsufficientData
		res.Reason = firstUnknownDetail(res.Conditions)
	}

	res.Summary = summarise(res.Conditions, truth)
	return res
}

// evalGroup evaluates a group, appending every leaf's result to out.
func (e *Evaluator) evalGroup(ctx *evalContext, nodes []Node, requireAll bool, out *[]ConditionResult) Tri {
	values := make([]Tri, 0, len(nodes))
	for _, n := range nodes {
		if n.IsGroup() {
			var nested Tri
			if len(n.Any) > 0 {
				nested = e.evalGroup(ctx, n.Any, false, out)
			} else {
				nested = e.evalGroup(ctx, n.All, true, out)
			}
			values = append(values, nested)
			continue
		}
		cr, t := e.evalCondition(ctx, n)
		*out = append(*out, cr)
		values = append(values, t)
	}
	if requireAll {
		return and(values)
	}
	return or(values)
}

// evalCondition evaluates one leaf comparison.
func (e *Evaluator) evalCondition(ctx *evalContext, n Node) (ConditionResult, Tri) {
	left := n.left()
	def, known := registry[left.Indicator]
	cr := ConditionResult{Op: n.Op}
	if known {
		cr.Label = def.Describe(left)
	} else {
		cr.Label = left.Indicator
	}

	// Resolve the right-hand side: a constant, or another indicator.
	var (
		rightLabel string
		rightAt    func(index int) (float64, string, bool)
	)
	if n.Value != nil {
		v := *n.Value
		rightLabel = formatNumber(v)
		rightAt = func(int) (float64, string, bool) { return v, "", true }
	} else if n.Compare != nil {
		o := *n.Compare
		if d, ok := registry[o.Indicator]; ok {
			rightLabel = d.Describe(o)
			if o.Mult != 0 && o.Mult != 1 {
				rightLabel = formatMultiplier(o.Mult) + "x" + rightLabel
			}
			if o.Offset != 0 {
				rightLabel = fmt.Sprintf("%s%+g", rightLabel, o.Offset)
			}
		} else {
			rightLabel = o.Indicator
		}
		rightAt = func(index int) (float64, string, bool) { return ctx.valueAt(o, index) }
	} else {
		cr.Result = TriUnknown.String()
		cr.Detail = "condition has no right-hand side"
		cr.Text = cr.Label + " " + n.Op + " ?"
		return cr, TriUnknown
	}
	cr.RightLabel = rightLabel

	lv, lWhy, lOK := ctx.valueAt(left, ctx.index)
	rv, rWhy, rOK := rightAt(ctx.index)
	cr.LeftValue, cr.RightValue = lv, rv

	if !lOK || !rOK {
		cr.Result = TriUnknown.String()
		cr.Detail = cmp.Or(lWhy, rWhy)
		cr.Text = fmt.Sprintf("%s %s %s (no data)", cr.Label, n.Op, rightLabel)
		return cr, TriUnknown
	}

	if isCrossOp(n.Op) {
		prev := ctx.index - 1
		if prev < 0 {
			cr.Result = TriUnknown.String()
			cr.Detail = "a crossover needs a previous bar to compare against"
			cr.Text = fmt.Sprintf("%s %s %s (no previous bar)", cr.Label, n.Op, rightLabel)
			return cr, TriUnknown
		}
		plv, plWhy, plOK := ctx.valueAt(left, prev)
		prv, prWhy, prOK := rightAt(prev)
		if !plOK || !prOK {
			cr.Result = TriUnknown.String()
			cr.Detail = cmp.Or(plWhy, prWhy)
			cr.Text = fmt.Sprintf("%s %s %s (previous bar has no data)", cr.Label, n.Op, rightLabel)
			return cr, TriUnknown
		}
		t := evalCross(n.Op, plv, prv, lv, rv)
		cr.Result = t.String()
		cr.Text = fmt.Sprintf("%s %s %s (%s -> %s vs %s -> %s)",
			cr.Label, humanOp(n.Op), rightLabel,
			formatNumber(plv), formatNumber(lv), formatNumber(prv), formatNumber(rv))
		return cr, t
	}

	t := evalCompare(n.Op, lv, rv)
	cr.Result = t.String()
	cr.Text = fmt.Sprintf("%s=%s %s %s", cr.Label, formatNumber(lv), n.Op, describeRight(rightLabel, rv, n.Value != nil))
	return cr, t
}

// evalCompare applies a same-bar comparison.
func evalCompare(op string, l, r float64) Tri {
	var ok bool
	switch op {
	case OpLT:
		ok = l < r
	case OpLTE:
		ok = l <= r
	case OpGT:
		ok = l > r
	case OpGTE:
		ok = l >= r
	case OpEQ:
		// Exact equality on floats is a trap, so compare within a relative
		// tolerance. An operator writing "== 35" means "is 35", not "is
		// bit-identical to 35 after two hundred smoothing steps".
		ok = nearlyEqual(l, r)
	case OpNEQ:
		ok = !nearlyEqual(l, r)
	default:
		return TriUnknown
	}
	if ok {
		return TriTrue
	}
	return TriFalse
}

// evalCross applies a crossover comparison.
//
// A crossover is a change of side between two consecutive bars, which is a
// different question from "is above". The previous bar may touch equality —
// a line resting exactly on another and then rising has crossed it — but the
// current bar must be strictly on the new side.
func evalCross(op string, prevLeft, prevRight, left, right float64) Tri {
	switch op {
	case OpCrossesAbove:
		if prevLeft <= prevRight && left > right {
			return TriTrue
		}
		return TriFalse
	case OpCrossesBelow:
		if prevLeft >= prevRight && left < right {
			return TriTrue
		}
		return TriFalse
	default:
		return TriUnknown
	}
}

// nearlyEqual compares with a relative tolerance, falling back to an absolute
// one near zero.
func nearlyEqual(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff < 1e-9 {
		return true
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	return diff <= scale*1e-9
}

func humanOp(op string) string {
	switch op {
	case OpCrossesAbove:
		return "crossed above"
	case OpCrossesBelow:
		return "crossed below"
	default:
		return op
	}
}

func describeRight(label string, value float64, isConstant bool) string {
	if isConstant {
		return label
	}
	return fmt.Sprintf("%s=%s", label, formatNumber(value))
}

// formatNumber renders a value at a precision that suits its magnitude, with
// thousands separators for large ones so alert text stays readable.
func formatNumber(v float64) string {
	if !indicators.IsDefined(v) {
		return "n/a"
	}
	abs := math.Abs(v)
	switch {
	case abs >= 1e7:
		return fmt.Sprintf("%.2fM", v/1e6)
	case abs >= 1000:
		// Share volumes are whole numbers in the hundreds of thousands;
		// rendering them with two decimal places is pure noise.
		if v == math.Trunc(v) && abs >= 1e5 {
			return addThousands(fmt.Sprintf("%.0f", v))
		}
		return addThousands(fmt.Sprintf("%.2f", v))
	case abs >= 1:
		return fmt.Sprintf("%.2f", v)
	case abs == 0:
		return "0"
	default:
		return fmt.Sprintf("%.4f", v)
	}
}

// formatMultiplier renders an operand scale factor compactly. A "1.5x average"
// rule should read as 1.5x, not 1.5000x.
func formatMultiplier(v float64) string {
	if !indicators.IsDefined(v) {
		return "n/a"
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	const maxDecimals = 4
	if dot := strings.IndexByte(s, '.'); dot >= 0 && len(s)-dot-1 > maxDecimals {
		s = strconv.FormatFloat(v, 'f', maxDecimals, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func addThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// summarise builds the plain-words explanation that leads a notification.
func summarise(conditions []ConditionResult, truth Tri) string {
	if len(conditions) == 0 {
		return "no conditions evaluated"
	}
	parts := make([]string, 0, len(conditions))
	for _, c := range conditions {
		// When the algorithm fired, the interesting fragments are the ones
		// that held; when it did not, show everything so the operator can see
		// what fell short.
		if truth == TriTrue && c.Result != TriTrue.String() {
			continue
		}
		parts = append(parts, c.Text)
	}
	if len(parts) == 0 {
		for _, c := range conditions {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, ", ")
}

func firstUnknownDetail(conditions []ConditionResult) string {
	for _, c := range conditions {
		if c.Result == TriUnknown.String() && c.Detail != "" {
			return c.Detail
		}
	}
	return "not enough data to decide"
}
