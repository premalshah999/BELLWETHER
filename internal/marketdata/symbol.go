package marketdata

import (
	"fmt"
	"strings"
)

// Exchange identifies the venue a ticker trades on. The zero value (ExchangeUS)
// means a US-listed symbol, which carries no suffix in canonical form.
type Exchange string

const (
	ExchangeUS  Exchange = ""
	ExchangeBSE Exchange = "BSE"
	ExchangeNSE Exchange = "NSE"
	// ExchangeIndex marks a benchmark index rather than a tradeable listing —
	// ^GSPC, ^VIX. It carries no currency of its own and exists only so a
	// chart or an event study can reference "the S&P 500" the same way it
	// references any other symbol.
	ExchangeIndex Exchange = "INDEX"
)

// Symbol is the canonical, provider-independent identity of an instrument.
//
// We standardise on the Alpha Vantage spelling because it is the one the
// operator types into the algorithm builder: RELIANCE.BSE, TCS.BSE, AAPL.
// Each adapter renders this into whatever dialect its upstream speaks, so no
// provider-specific string ever escapes into storage or the API surface.
type Symbol struct {
	Ticker   string
	Exchange Exchange
}

// ParseSymbol reads a canonical symbol string. It is deliberately lenient about
// case and whitespace because the string arrives from a text input.
func ParseSymbol(s string) (Symbol, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return Symbol{}, fmt.Errorf("empty symbol")
	}

	// "^GSPC" is the conventional way to type an index, so it is accepted on
	// input even though it is never the stored form — the caret is not a
	// character validTicker allows, because admitting it there would make it
	// mean two different things depending on position.
	if strings.HasPrefix(s, "^") {
		ticker := strings.TrimPrefix(s, "^")
		if !validTicker(ticker) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: ticker, Exchange: ExchangeIndex}, nil
	}

	ticker, suffix, found := strings.Cut(s, ".")
	if ticker == "" {
		return Symbol{}, fmt.Errorf("symbol %q has no ticker", s)
	}
	if !found {
		if !validTicker(ticker) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: ticker, Exchange: ExchangeUS}, nil
	}
	switch Exchange(suffix) {
	case ExchangeBSE:
		if !validTicker(ticker) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: ticker, Exchange: ExchangeBSE}, nil
	case ExchangeNSE:
		if !validTicker(ticker) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: ticker, Exchange: ExchangeNSE}, nil
	case ExchangeIndex:
		if !validTicker(ticker) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: ticker, Exchange: ExchangeIndex}, nil
	}
	// A single letter after the dot is a class share, not a venue — SEC
	// spells these with a hyphen (BRK-B) and that is the canonical storage
	// form, but a person types the dot ("BRK.B") because that is how every
	// broker and news site prints it. Rewriting here means the venue
	// separator ("." between ticker and BSE/NSE/INDEX) never has to also
	// mean "class share", which would make the two impossible to tell apart
	// for a ticker that happened to be one letter short of an exchange code.
	if len(suffix) == 1 && suffix[0] >= 'A' && suffix[0] <= 'Z' {
		rewritten := ticker + "-" + suffix
		if !validTicker(rewritten) {
			return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
		}
		return Symbol{Ticker: rewritten, Exchange: ExchangeUS}, nil
	}
	return Symbol{}, fmt.Errorf("symbol %q: unknown exchange %q (want BSE, NSE, INDEX, a single-letter class suffix, or no suffix for US)", s, suffix)
}

// validTicker reports whether a ticker is plausibly a listed instrument.
//
// Without this the parser accepted anything non-empty, so a bulk paste with a
// stray line of prose in it put that line on the watchlist as an instrument
// and the system then tried to price it every few seconds.
//
// Ampersands and hyphens are permitted because real Indian tickers use them —
// M&M, M&MFIN, L&TFH, BAJAJ-AUTO — and a rule that rejected those would be
// worse than no rule. At least one letter is required so a bare number is not
// mistaken for a ticker.
func validTicker(t string) bool {
	if len(t) == 0 || len(t) > 20 {
		return false
	}
	letters := 0
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
		case r >= '0' && r <= '9', r == '&', r == '-':
		default:
			return false
		}
	}
	return letters > 0
}

// MustParseSymbol is ParseSymbol for package-level fixtures and seed data.
func MustParseSymbol(s string) Symbol {
	sym, err := ParseSymbol(s)
	if err != nil {
		panic(err)
	}
	return sym
}

// String renders the canonical form. This is what goes in the database and on
// the wire to the frontend.
func (s Symbol) String() string {
	if s.Exchange == ExchangeUS {
		return s.Ticker
	}
	return s.Ticker + "." + string(s.Exchange)
}

// IsIndex reports whether the symbol is a benchmark index rather than a
// tradeable instrument. Screens, watchlists and peer comparisons all need to
// exclude these; an index has no fundamentals and is not a position.
func (s Symbol) IsIndex() bool {
	return s.Exchange == ExchangeIndex
}

// IsIndian reports whether the symbol trades on an Indian venue. Used for
// market-hours and currency decisions.
func (s Symbol) IsIndian() bool {
	return s.Exchange == ExchangeBSE || s.Exchange == ExchangeNSE
}

// Currency is the quote currency implied by the listing venue.
func (s Symbol) Currency() string {
	if s.IsIndian() {
		return "INR"
	}
	return "USD"
}
