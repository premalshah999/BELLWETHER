package marketdata

import (
	"fmt"
	"strings"
)

// Exchange identifies what kind of instrument a symbol names. The zero value
// is a US listing, which carries no suffix.
type Exchange string

const (
	ExchangeUS Exchange = ""
	// ExchangeIndex marks a benchmark index (GSPC.INDEX, typed as ^GSPC), so
	// a chart or an event study can reference the S&P 500 like any symbol.
	ExchangeIndex Exchange = "INDEX"
)

// Symbol is the canonical, provider-independent identity of an instrument.
// Each adapter renders it into its upstream's dialect, so no vendor spelling
// reaches storage or the API.
type Symbol struct {
	Ticker   string
	Exchange Exchange
}

// ParseSymbol reads a canonical symbol, leniently about case and spacing
// because it arrives from a text input.
func ParseSymbol(s string) (Symbol, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return Symbol{}, fmt.Errorf("empty symbol")
	}
	ticker, exchange := s, ExchangeUS
	switch head, suffix, found := strings.Cut(s, "."); {
	case strings.HasPrefix(s, "^"):
		ticker, exchange = s[1:], ExchangeIndex
	case !found:
	case suffix == string(ExchangeIndex):
		ticker, exchange = head, ExchangeIndex
	case len(suffix) == 1:
		// A class share: people type BRK.B, SEC and storage spell it BRK-B.
		ticker = head + "-" + suffix
	default:
		return Symbol{}, fmt.Errorf("symbol %q: unknown exchange %q (US tickers take no suffix; indices end in .INDEX)", s, suffix)
	}
	if !validTicker(ticker) {
		return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
	}
	return Symbol{Ticker: ticker, Exchange: exchange}, nil
}

// validTicker rejects anything that cannot be a listing, so a stray line of
// prose in a bulk paste is not priced every few seconds: letters, digits and
// the class-share hyphen, with at least one letter.
func validTicker(t string) bool {
	if len(t) == 0 || len(t) > 20 {
		return false
	}
	letters := 0
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
		case r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return letters > 0
}

// MustParseSymbol is ParseSymbol for fixtures and seed data.
func MustParseSymbol(s string) Symbol {
	sym, err := ParseSymbol(s)
	if err != nil {
		panic(err)
	}
	return sym
}

// String renders the canonical form stored in the database and sent to the
// frontend.
func (s Symbol) String() string {
	if s.Exchange == ExchangeUS {
		return s.Ticker
	}
	return s.Ticker + "." + string(s.Exchange)
}

// IsIndex reports whether the symbol is a benchmark rather than something
// that can be held, screened or compared on fundamentals.
func (s Symbol) IsIndex() bool { return s.Exchange == ExchangeIndex }

// Currency is the quote currency. Every covered listing trades in dollars.
func (s Symbol) Currency() string { return "USD" }
