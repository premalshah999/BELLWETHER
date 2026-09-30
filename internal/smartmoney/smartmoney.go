// Package smartmoney tracks what company insiders and well-known investors
// buy and sell, from the filings they are legally required to make: SEC
// Form 4 for officers, directors and 10% owners (due within two business
// days of a trade), and SEC 13F for institutional managers (quarterly,
// within 45 days of quarter end).
package smartmoney

import (
	"context"
	"time"
)

// Trade is one line of a Form 4: one transaction by one insider.
type Trade struct {
	Accession    string    `json:"accession"`
	Line         int       `json:"-"`
	Symbol       string    `json:"symbol"`
	IssuerCIK    string    `json:"issuer_cik"`
	IssuerName   string    `json:"issuer_name"`
	OwnerCIK     string    `json:"owner_cik"`
	OwnerName    string    `json:"owner_name"`
	IsDirector   bool      `json:"is_director"`
	IsOfficer    bool      `json:"is_officer"`
	IsTenPct     bool      `json:"is_ten_pct"`
	OfficerTitle string    `json:"officer_title,omitempty"`
	Security     string    `json:"security"`
	TxDate       time.Time `json:"tx_date"`
	// Code is the SEC transaction code: P open-market purchase, S sale,
	// A grant, M option exercise, F shares withheld for tax, G gift, and
	// others. Only P and S are discretionary trades at market prices.
	Code       string   `json:"code"`
	Acquired   bool     `json:"acquired"`
	Shares     float64  `json:"shares"`
	Price      *float64 `json:"price,omitempty"`
	Value      *float64 `json:"value,omitempty"`
	OwnedAfter *float64 `json:"owned_after,omitempty"`
	Direct     bool     `json:"direct"`
	// Plan105b1 is set when the filing says the trade was made under a
	// pre-arranged 10b5-1 plan, which makes it far less informative.
	Plan105b1 bool      `json:"plan_10b5_1"`
	FiledAt   time.Time `json:"filed_at"`
}

// Role is how a reader should describe the insider.
func (t Trade) Role() string {
	switch {
	case t.OfficerTitle != "":
		return t.OfficerTitle
	case t.IsOfficer:
		return "Officer"
	case t.IsDirector:
		return "Director"
	case t.IsTenPct:
		return "10% owner"
	}
	return "Insider"
}

// Fund is a manager this app follows.
type Fund struct {
	CIK     string `json:"cik"`
	Name    string `json:"name"`
	Manager string `json:"manager"`
	Style   string `json:"style"`
}

// Holding is one position in a 13F, summed across the filer's sub-managers.
type Holding struct {
	CUSIP   string  `json:"cusip"`
	Issuer  string  `json:"issuer"`
	Symbol  string  `json:"symbol"`
	Value   float64 `json:"value"`
	Shares  float64 `json:"shares"`
	PutCall string  `json:"put_call,omitempty"`
}

// FundFiling is one quarterly 13F.
type FundFiling struct {
	Accession  string    `json:"accession"`
	CIK        string    `json:"cik"`
	Period     time.Time `json:"period"`
	Filed      time.Time `json:"filed"`
	TotalValue float64   `json:"total_value"`
	Positions  int       `json:"positions"`
}

// Store is what the sync needs from persistence.
type Store interface {
	InsiderFilingSeen(ctx context.Context, accession string) (bool, error)
	MarkInsiderFiling(ctx context.Context, accession string, ok bool) error
	SaveInsiderTrades(ctx context.Context, trades []Trade) error

	UpsertFund(ctx context.Context, f Fund) error
	FundFilingExists(ctx context.Context, accession string) (bool, error)
	SaveFundFiling(ctx context.Context, f FundFiling, holdings []Holding) error
	CUSIPSymbols(ctx context.Context, cusips []string) (map[string]string, error)
	SaveCUSIPSymbols(ctx context.Context, m map[string]string) error
}

// Funds is the list of managers followed: widely watched investors whose
// quarterly moves are news in themselves.
var Funds = []Fund{
	{CIK: "0001067983", Name: "Berkshire Hathaway", Manager: "Warren Buffett", Style: "Value"},
	{CIK: "0001336528", Name: "Pershing Square", Manager: "Bill Ackman", Style: "Activist"},
	{CIK: "0001649339", Name: "Scion Asset Management", Manager: "Michael Burry", Style: "Contrarian"},
	{CIK: "0001536411", Name: "Duquesne Family Office", Manager: "Stanley Druckenmiller", Style: "Macro"},
	{CIK: "0001029160", Name: "Soros Fund Management", Manager: "George Soros", Style: "Macro"},
	{CIK: "0001350694", Name: "Bridgewater Associates", Manager: "Ray Dalio", Style: "Macro"},
	{CIK: "0001040273", Name: "Third Point", Manager: "Daniel Loeb", Style: "Activist"},
	{CIK: "0001656456", Name: "Appaloosa", Manager: "David Tepper", Style: "Opportunistic"},
	{CIK: "0001167483", Name: "Tiger Global", Manager: "Chase Coleman", Style: "Growth"},
	{CIK: "0001061768", Name: "Baupost Group", Manager: "Seth Klarman", Style: "Value"},
	{CIK: "0001697748", Name: "ARK Investment", Manager: "Cathie Wood", Style: "Innovation"},
	{CIK: "0000921669", Name: "Icahn Capital", Manager: "Carl Icahn", Style: "Activist"},
	{CIK: "0001037389", Name: "Renaissance Technologies", Manager: "Jim Simons' firm", Style: "Quant"},
	{CIK: "0001103804", Name: "Viking Global", Manager: "Andreas Halvorsen", Style: "Long/short"},
}

// Leader is one company's insider activity over a window, counting only
// open-market purchases (P) and sales (S): grants, option exercises and tax
// withholding say nothing about what an insider thinks the stock is worth.
type Leader struct {
	Symbol     string    `json:"symbol"`
	Issuer     string    `json:"issuer"`
	BuyValue   float64   `json:"buy_value"`
	SellValue  float64   `json:"sell_value"`
	Buyers     int       `json:"buyers"`
	Sellers    int       `json:"sellers"`
	Buys       int       `json:"buys"`
	Sells      int       `json:"sells"`
	LastTrade  time.Time `json:"last_trade"`
	BuyerNames []string  `json:"buyer_names,omitempty"`
}

// Move is one fund's change in one position between its last two 13Fs.
type Move struct {
	FundCIK     string  `json:"fund_cik"`
	FundName    string  `json:"fund_name"`
	Manager     string  `json:"manager"`
	Symbol      string  `json:"symbol"`
	Issuer      string  `json:"issuer"`
	CUSIP       string  `json:"cusip"`
	Value       float64 `json:"value"`
	Shares      float64 `json:"shares"`
	PrevShares  float64 `json:"prev_shares"`
	PrevValue   float64 `json:"prev_value"`
	Kind        string  `json:"kind"` // new | added | trimmed | exited | held
	ChangePct   float64 `json:"change_pct"`
	WeightPct   float64 `json:"weight_pct"`
	PeriodLabel string  `json:"period"`
}

// FundSummary is a followed fund with its latest filing.
type FundSummary struct {
	Fund
	Period     *time.Time `json:"period,omitempty"`
	Filed      *time.Time `json:"filed,omitempty"`
	TotalValue float64    `json:"total_value"`
	Positions  int        `json:"positions"`
	Top        []Move     `json:"top"`
	New        int        `json:"new_positions"`
	Exited     int        `json:"exited_positions"`
}
