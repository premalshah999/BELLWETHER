package postgres

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// stringArray adapts a Go []string to a Postgres TEXT[], which database/sql
// has no notion of. Elements are quoted only where Postgres itself would quote
// them; getting that wrong is silent (a symbol containing a comma would split
// in two), so the round trip is tested.
type stringArray []string

// Value renders the slice as a Postgres array literal.
func (a stringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	if len(a) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		if needsQuoting(s) {
			b.WriteByte('"')
			for _, r := range s {
				if r == '"' || r == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(r)
			}
			b.WriteByte('"')
			continue
		}
		b.WriteString(s)
	}
	b.WriteByte('}')
	return b.String(), nil
}

func needsQuoting(s string) bool {
	// An empty element must be quoted, or it disappears. So must anything
	// Postgres would otherwise read as structure, and the literal NULL, which
	// unquoted means a null element rather than the four characters.
	if s == "" || strings.EqualFold(s, "null") {
		return true
	}
	return strings.ContainsAny(s, `{},"\ `+"\t\n\r")
}

// Scan parses a Postgres array literal into the slice.
func (a *stringArray) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		return a.parse(v)
	case []byte:
		return a.parse(string(v))
	default:
		return fmt.Errorf("postgres: cannot scan %T into a text array", src)
	}
}

func (a *stringArray) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		*a = []string{}
		return nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return fmt.Errorf("postgres: malformed array literal %q", s)
	}
	s = s[1 : len(s)-1]

	// Whether an element was quoted has to be tracked, not inferred from its
	// value afterwards. Unquoted NULL is a null element; quoted "NULL" is the
	// four-character string, and a parser that decides after unquoting cannot
	// tell them apart — it would silently drop a legitimate value.
	type element struct {
		text   string
		quoted bool
	}
	var (
		out     []element
		cur     strings.Builder
		curQ    bool
		inQuote bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
			curQ = true
		case r == ',' && !inQuote:
			out = append(out, element{cur.String(), curQ})
			cur.Reset()
			curQ = false
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, element{cur.String(), curQ})

	cleaned := make([]string, 0, len(out))
	for _, e := range out {
		if !e.quoted && e.text == "NULL" {
			continue
		}
		cleaned = append(cleaned, e.text)
	}
	*a = cleaned
	return nil
}

// int64Array adapts a slice of ids to a Postgres BIGINT[].
//
// The driver has no default mapping for []int64, so without this a watchlist
// attachment fails at the wire with a type error rather than anywhere useful.
type int64Array []int64

func (a int64Array) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(a))
	for i, v := range a {
		parts[i] = strconv.FormatInt(v, 10)
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// Scan reads a BIGINT[] back. Malformed entries are skipped rather than
// failing the row: one unparseable id should not hide an algorithm.
func (a *int64Array) Scan(src any) error {
	*a = nil
	var raw string
	switch v := src.(type) {
	case nil:
		return nil
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("int64Array: cannot scan %T", src)
	}
	raw = strings.Trim(raw, "{}")
	if raw == "" {
		return nil
	}
	for _, p := range strings.Split(raw, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			*a = append(*a, n)
		}
	}
	return nil
}
