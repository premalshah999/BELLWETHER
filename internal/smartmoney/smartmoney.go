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
	// Curated marks a manager from the shipped list rather than one the
	// operator found and followed.
	Curated bool `json:"curated"`
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
	InsiderFilingsSeen(ctx context.Context, accessions []string) (map[string]bool, error)
	MarkInsiderFilings(ctx context.Context, accessions []string) error
	SaveInsiderTrades(ctx context.Context, trades []Trade) error

	UpsertFund(ctx context.Context, f Fund) error
	FollowedFunds(ctx context.Context) ([]Fund, error)
	UnresolvedCUSIPs(ctx context.Context, limit int) ([]string, error)
	FundFilingExists(ctx context.Context, accession string) (bool, error)
	SaveFundFiling(ctx context.Context, f FundFiling, holdings []Holding) error
	CUSIPSymbols(ctx context.Context, cusips []string) (map[string]string, error)
	SaveCUSIPSymbols(ctx context.Context, m map[string]string) error
}

// Funds is the shipped list of managers: widely watched investors whose
// quarterly moves are news in themselves, plus the largest institutions and
// sovereign funds. The operator can follow any other 13F filer by name.
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
	{CIK: "0001061165", Name: "Lone Pine Capital LLC", Manager: "Stephen Mandel", Style: "Growth"},
	{CIK: "0001135730", Name: "Coatue Management LLC", Manager: "Philippe Laffont", Style: "Growth"},
	{CIK: "0001423053", Name: "Citadel Advisors LLC", Manager: "Ken Griffin", Style: "Multi-strategy"},
	{CIK: "0001179392", Name: "Two Sigma Investments, LP", Manager: "John Overdeck & David Siegel", Style: "Quant"},
	{CIK: "0001009207", Name: "D. E. Shaw & Co., Inc.", Manager: "David Shaw", Style: "Quant"},
	{CIK: "0001273087", Name: "Millennium Management LLC", Manager: "Israel Englander", Style: "Multi-strategy"},
	{CIK: "0001603466", Name: "Point72 Asset Management, L.P.", Manager: "Steve Cohen", Style: "Multi-strategy"},
	{CIK: "0001791786", Name: "Elliott Investment Management L.P.", Manager: "Paul Singer", Style: "Activist"},
	{CIK: "0001517137", Name: "Starboard Value LP", Manager: "Jeff Smith", Style: "Activist"},
	{CIK: "0001345471", Name: "Trian Fund Management, L.P.", Manager: "Nelson Peltz", Style: "Activist"},
	{CIK: "0000934639", Name: "Maverick Capital Ltd", Manager: "Lee Ainslie", Style: "Long/short"},
	{CIK: "0001138995", Name: "Glenview Capital Management, LLC", Manager: "Larry Robbins", Style: "Long/short"},
	{CIK: "0001709323", Name: "Himalaya Capital Management LLC", Manager: "Li Lu", Style: "Value"},
	{CIK: "0001549575", Name: "Dalal Street, LLC", Manager: "Mohnish Pabrai", Style: "Value"},
	{CIK: "0001112520", Name: "Akre Capital Management LLC", Manager: "Chuck Akre", Style: "Quality"},
	{CIK: "0001720792", Name: "Ruane, Cunniff & Goldfarb L.P.", Manager: "Sequoia Fund", Style: "Value"},
	{CIK: "0000732905", Name: "Tweedy, Browne Co LLC", Manager: "Tweedy Browne", Style: "Value"},
	{CIK: "0001096343", Name: "Markel Group Inc.", Manager: "Tom Gayner", Style: "Value"},
	{CIK: "0001569205", Name: "Fundsmith LLP", Manager: "Terry Smith", Style: "Quality"},
	{CIK: "0000949509", Name: "Oaktree Capital Management LP", Manager: "Howard Marks", Style: "Credit & value"},
	{CIK: "0001798849", Name: "Durable Capital Partners LP", Manager: "Henry Ellenbogen", Style: "Growth"},
	{CIK: "0001541617", Name: "Altimeter Capital Management, LP", Manager: "Brad Gerstner", Style: "Growth"},
	{CIK: "0001387322", Name: "Whale Rock Capital Management LLC", Manager: "Alex Sacerdote", Style: "Growth"},
	{CIK: "0001602189", Name: "Dragoneer Investment Group, LLC", Manager: "Marc Stad", Style: "Growth"},
	{CIK: "0000909661", Name: "Farallon Capital Management, L.L.C.", Manager: "Andrew Spokes", Style: "Multi-strategy"},
	{CIK: "0001358706", Name: "Abrams Capital Management, L.P.", Manager: "David Abrams", Style: "Value"},
	{CIK: "0001510387", Name: "Gotham Asset Management, LLC", Manager: "Joel Greenblatt", Style: "Quant value"},
	{CIK: "0001056831", Name: "Fairholme Capital Management LLC", Manager: "Bruce Berkowitz", Style: "Value"},
	{CIK: "0000807985", Name: "Southeastern Asset Management", Manager: "Mason Hawkins", Style: "Value"},
	{CIK: "0001034524", Name: "Polen Capital Management LLC", Manager: "Dan Davidowitz", Style: "Quality growth"},
	{CIK: "0001088875", Name: "Baillie Gifford & Co", Manager: "Baillie Gifford", Style: "Growth"},
	{CIK: "0000102909", Name: "Vanguard Group Inc", Manager: "Vanguard", Style: "Index"},
	{CIK: "0002012383", Name: "BlackRock, Inc.", Manager: "BlackRock", Style: "Index"},
	{CIK: "0001374170", Name: "Norges Bank", Manager: "Norway's sovereign fund", Style: "Sovereign"},
	{CIK: "0001767640", Name: "Public Investment Fund", Manager: "Saudi PIF", Style: "Sovereign"},
	{CIK: "0001082621", Name: "Harvard Management Co", Manager: "Harvard endowment", Style: "Endowment"},
	{CIK: "0000860643", Name: "Gardner Russo & Quinn LLC", Manager: "Tom Russo", Style: "Value"},
	{CIK: "0000883965", Name: "Weitz Investment Management, Inc.", Manager: "Wallace Weitz", Style: "Value"},
	{CIK: "0000915191", Name: "Fairfax Financial Holdings Ltd", Manager: "Prem Watsa", Style: "Value"},
	{CIK: "0001135778", Name: "Miller Value Partners, LLC", Manager: "Bill Miller", Style: "Value"},
	{CIK: "0001553733", Name: "Brave Warrior Advisors, LLC", Manager: "Glenn Greenberg", Style: "Value"},
	{CIK: "0001647251", Name: "TCI Fund Management Ltd", Manager: "Chris Hohn", Style: "Activist"},
	{CIK: "0000923093", Name: "Tudor Investment Corp", Manager: "Paul Tudor Jones", Style: "Macro"},
	{CIK: "0000850529", Name: "Fisher Asset Management, LLC", Manager: "Ken Fisher", Style: "Growth"},
	{CIK: "0002045724", Name: "Situational Awareness LP", Manager: "Leopold Aschenbrenner", Style: "AI thematic"},
	{CIK: "0001569049", Name: "Light Street Capital Management, LLC", Manager: "Glen Kacher", Style: "Tech"},
	{CIK: "0001517857", Name: "Soroban Capital Partners LP", Manager: "Eric Mandelblatt", Style: "Long/short"},
	{CIK: "0001279936", Name: "Cantillon Capital Management LLC", Manager: "William von Mueffling", Style: "Quality"},
	{CIK: "0001581811", Name: "Egerton Capital (UK) LLP", Manager: "John Armitage", Style: "Long/short"},
	{CIK: "0001027796", Name: "Pzena Investment Management LLC", Manager: "Richard Pzena", Style: "Value"},
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

// Classify sets a move's kind, change and weight from the two quarters'
// share counts. Without a previous filing every position is simply held.
func Classify(m *Move, hasPrev bool, totalValue float64) {
	switch {
	case !hasPrev:
		m.Kind = "held"
	case m.PrevShares == 0 && m.Shares > 0:
		m.Kind = "new"
	case m.Shares == 0 && m.PrevShares > 0:
		m.Kind = "exited"
	case m.Shares > m.PrevShares*1.02:
		m.Kind = "added"
	case m.Shares < m.PrevShares*0.98:
		m.Kind = "trimmed"
	default:
		m.Kind = "held"
	}
	if m.PrevShares > 0 {
		m.ChangePct = (m.Shares - m.PrevShares) / m.PrevShares * 100
	}
	if totalValue > 0 {
		m.WeightPct = m.Value / totalValue * 100
	}
}
