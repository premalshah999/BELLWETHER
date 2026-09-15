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
	ticker, suffix, found := strings.Cut(s, ".")
	if ticker == "" {
		return Symbol{}, fmt.Errorf("symbol %q has no ticker", s)
	}
	if !validTicker(ticker) {
		return Symbol{}, fmt.Errorf("symbol %q is not a ticker", s)
	}
	if !found {
		return Symbol{Ticker: ticker, Exchange: ExchangeUS}, nil
	}
	switch Exchange(suffix) {
	case ExchangeBSE:
		return Symbol{Ticker: ticker, Exchange: ExchangeBSE}, nil
	case ExchangeNSE:
		return Symbol{Ticker: ticker, Exchange: ExchangeNSE}, nil
	default:
		return Symbol{}, fmt.Errorf("symbol %q: unknown exchange %q (want BSE, NSE, or no suffix for US)", s, suffix)
	}
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
