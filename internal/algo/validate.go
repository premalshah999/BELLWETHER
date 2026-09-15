package algo

import (
	"fmt"
	"strings"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Comparison operators.
const (
	OpLT           = "<"
	OpLTE          = "<="
	OpGT           = ">"
	OpGTE          = ">="
	OpEQ           = "=="
	OpNEQ          = "!="
	OpCrossesAbove = "crosses_above"
	OpCrossesBelow = "crosses_below"
)

// maxSymbols bounds one algorithm's watch set. Each symbol costs a provider
// request per evaluation, so an algorithm covering hundreds of names would
// quietly exhaust the day's budget.
const maxSymbols = 50

// maxConditionsPerGroup bounds a group's size, keeping alert text readable and
// evaluation bounded.
const maxConditionsPerGroup = 20

// maxPeriod bounds any lookback window. Anything larger asks for more history
// than the providers carry.
const maxPeriod = 2000

// isCrossOp reports whether an operator compares two consecutive bars rather
// than a single one.
func isCrossOp(op string) bool { return op == OpCrossesAbove || op == OpCrossesBelow }

// Validate checks the whole algorithm and reports every problem it finds.
//
// Validation runs before an algorithm is ever stored, so a rule set that
// reaches the scheduler is known to be evaluable. It also normalises defaults
// in place — filling in RSI's period of 14, MACD's 12/26/9 — so the evaluator
// never has to guess.
func (a *Algorithm) Validate() error {
	var errs ValidationErrors

	add := func(field, format string, args ...any) {
		errs = append(errs, &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(a.Name) == "" {
		add("name", "an algorithm needs a name")
	} else if len(a.Name) > 120 {
		add("name", "name is %d characters; the maximum is 120", len(a.Name))
	}
	a.Name = strings.TrimSpace(a.Name)

	switch {
	case len(a.Symbols) == 0 && len(a.WatchlistIDs) == 0:
		// A rule needs something to evaluate, but it may name instruments
		// directly or point at a list. Requiring symbols even when a list is
		// attached would defeat the point of attaching one.
		add("symbols", "name at least one symbol, or attach a watchlist")
	case len(a.Symbols) > maxSymbols:
		add("symbols", "%d symbols exceeds the limit of %d; each one costs a provider request per evaluation",
			len(a.Symbols), maxSymbols)
	}
	seen := map[string]bool{}
	for i, raw := range a.Symbols {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			add(fmt.Sprintf("symbols[%d]", i), "%s", err.Error())
			continue
		}
		canonical := sym.String()
		if seen[canonical] {
			add(fmt.Sprintf("symbols[%d]", i), "%s is listed more than once", canonical)
			continue
		}
		seen[canonical] = true
		// Normalise so the scheduler and the alert text agree on spelling.
		a.Symbols[i] = canonical
	}

	if a.Interval == "" {
		add("interval", "choose an interval, for example \"1d\"")
	} else if _, err := marketdata.ParseInterval(a.Interval); err != nil {
		add("interval", "%s", err.Error())
	}

	switch {
	case len(a.All) > 0 && len(a.Any) > 0:
		add("", `set either "all" or "any" at the top level, not both`)
	case len(a.All) == 0 && len(a.Any) == 0:
		add("", `an algorithm needs at least one condition under "all" or "any"`)
	}

	if len(a.All) > 0 {
		errs = append(errs, validateGroup(a.All, "all", 0)...)
	}
	if len(a.Any) > 0 {
		errs = append(errs, validateGroup(a.Any, "any", 0)...)
	}

	if a.CooldownHours < 0 {
		add("cooldown_hours", "cooldown cannot be negative")
	}
	if a.CooldownHours > 24*365 {
		add("cooldown_hours", "cooldown of %g hours is longer than a year", a.CooldownHours)
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validateGroup checks one condition group. depth 0 is the top level; nested
// groups are permitted at depth 1 and no further.
func validateGroup(nodes []Node, path string, depth int) ValidationErrors {
	var errs ValidationErrors

	if len(nodes) > maxConditionsPerGroup {
		errs = append(errs, &ValidationError{
			Field:   path,
			Message: fmt.Sprintf("%d conditions exceeds the limit of %d", len(nodes), maxConditionsPerGroup),
		})
		return errs
	}

	for i := range nodes {
		field := fmt.Sprintf("%s[%d]", path, i)
		errs = append(errs, validateNode(&nodes[i], field, depth)...)
	}
	return errs
}

func validateNode(n *Node, field string, depth int) ValidationErrors {
	var errs ValidationErrors
	add := func(f, format string, args ...any) {
		errs = append(errs, &ValidationError{Field: f, Message: fmt.Sprintf(format, args...)})
	}

	isGroup := n.IsGroup()
	isLeaf := n.Indicator != "" || n.Op != ""

	switch {
	case isGroup && isLeaf:
		add(field, `a node is either a group ("all"/"any") or a condition, not both`)
		return errs
	case !isGroup && !isLeaf:
		add(field, "this condition is empty")
		return errs
	}

	if isGroup {
		if len(n.All) > 0 && len(n.Any) > 0 {
			add(field, `set either "all" or "any" on a group, not both`)
			return errs
		}
		if depth >= 1 {
			add(field, "condition groups may be nested only one level deep")
			return errs
		}
		if len(n.All) > 0 {
			errs = append(errs, validateGroup(n.All, field+".all", depth+1)...)
		}
		if len(n.Any) > 0 {
			errs = append(errs, validateGroup(n.Any, field+".any", depth+1)...)
		}
		return errs
	}

	// Leaf condition. Validate the flattened left operand, then write the
	// normalised parameters back onto the node so the evaluator sees the
	// filled-in defaults rather than zeroes.
	left := n.left()
	errs = append(errs, validateOperand(&left, field)...)
	n.Period, n.Fast, n.Slow, n.Signal = left.Period, left.Fast, left.Slow, left.Signal

	if n.Op == "" {
		add(field+".op", "choose a comparison operator")
	} else if !validOp(n.Op) {
		add(field+".op", "unknown operator %q; expected one of %s", n.Op, strings.Join(Operators(), ", "))
	}

	switch {
	case n.Value != nil && n.Compare != nil:
		add(field, `set either "value" or "compare", not both`)
	case n.Value == nil && n.Compare == nil:
		add(field, `set "value" for a constant or "compare" for another indicator`)
	case n.Compare != nil:
		errs = append(errs, validateOperand(n.Compare, field+".compare")...)
	}

	return errs
}

func validOp(op string) bool {
	for _, valid := range Operators() {
		if op == valid {
			return true
		}
	}
	return false
}

// validateOperand checks an indicator reference and fills in its defaults.
func validateOperand(o *Operand, field string) ValidationErrors {
	var errs ValidationErrors
	add := func(f, format string, args ...any) {
		errs = append(errs, &ValidationError{Field: f, Message: fmt.Sprintf(format, args...)})
	}

	if o.Indicator == "" {
		add(field+".indicator", "choose an indicator")
		return errs
	}
	def, ok := registry[o.Indicator]
	if !ok {
		add(field+".indicator", "unknown indicator %q; expected one of %s",
			o.Indicator, strings.Join(IndicatorNames(), ", "))
		return errs
	}

	switch def.Kind {
	case paramNone:
		if o.Period != 0 {
			add(field+".period", "%q takes no period", o.Indicator)
		}
		if o.Fast != 0 || o.Slow != 0 || o.Signal != 0 {
			add(field, "%q takes no fast/slow/signal parameters", o.Indicator)
		}

	case paramPeriod:
		if o.Period == 0 {
			if def.DefaultPeriod == 0 {
				add(field+".period", "%q requires a period", o.Indicator)
			} else {
				o.Period = def.DefaultPeriod
			}
		}
		if o.Period < 0 {
			add(field+".period", "period must be positive, got %d", o.Period)
		} else if o.Period == 1 && o.Indicator == "rsi" {
			// RSI(1) is degenerate: it is either 0 or 100 on every bar.
			add(field+".period", "an RSI period of 1 is degenerate; use 2 or more")
		} else if o.Period > maxPeriod {
			add(field+".period", "period %d exceeds the maximum of %d", o.Period, maxPeriod)
		}
		if o.Fast != 0 || o.Slow != 0 || o.Signal != 0 {
			add(field, "%q takes a period, not fast/slow/signal", o.Indicator)
		}

	case paramMACD:
		if o.Fast == 0 {
			o.Fast = 12
		}
		if o.Slow == 0 {
			o.Slow = 26
		}
		if o.Signal == 0 {
			o.Signal = 9
		}
		if o.Fast <= 0 || o.Slow <= 0 || o.Signal <= 0 {
			add(field, "MACD periods must all be positive, got fast=%d slow=%d signal=%d", o.Fast, o.Slow, o.Signal)
		} else if o.Fast >= o.Slow {
			add(field, "the fast period (%d) must be shorter than the slow period (%d)", o.Fast, o.Slow)
		}
		if o.Slow > maxPeriod || o.Signal > maxPeriod {
			add(field, "MACD periods exceed the maximum of %d", maxPeriod)
		}
		if o.Period != 0 {
			add(field+".period", "%q takes fast/slow/signal, not a period", o.Indicator)
		}
	}

	if o.Shift < 0 {
		add(field+".shift", "shift cannot be negative, got %d", o.Shift)
	} else if o.Shift > maxPeriod {
		add(field+".shift", "shift %d exceeds the maximum of %d", o.Shift, maxPeriod)
	}

	if o.Mult != 0 && o.Mult < 0 {
		add(field+".mult", "a negative multiplier (%g) inverts the comparison; express that with the operator instead", o.Mult)
	}
	return errs
}
