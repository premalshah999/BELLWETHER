package ai

import "github.com/tradesys/dashboard/internal/events"

// typeCriteria describes each event type for Jev's choice question.
//
// A choice is only as good as the descriptions it chooses between: Jev reads
// these to decide which option the state fits, so each one says what the type
// is and, where two are easily confused, what separates them. Written for US
// markets. The types that only ever described an Indian exchange filing still
// appear -- the validator accepts them, and keeping the question and the
// validator on one list is what stops them disagreeing -- but they are
// described in terms that make them rare, which they should be.
var typeCriteria = map[events.Type]string{
	events.TypeEarnings:    "Reported quarterly or annual results: revenue, profit, EPS actually announced.",
	events.TypeGuidance:    "A forecast of future results raised, cut, reaffirmed or withdrawn. Not results already reported.",
	events.TypeDividend:    "A cash dividend declared, raised, cut or suspended.",
	events.TypeBonus:       "A stock dividend or bonus share issue, paid in shares rather than cash.",
	events.TypeStockSplit:  "A stock split or reverse stock split.",
	events.TypeBuyback:     "A share repurchase programme authorised, expanded or executed.",
	events.TypeRightsIssue: "A rights offering to existing shareholders.",
	events.TypeRecordDate:  "Only a record or ex-date notice for an action announced elsewhere, with nothing new.",

	events.TypeOrderWin:       "A specific contract, order or award won, usually with a value.",
	events.TypeOrderCancelled: "A contract, order or deal cancelled, terminated or walked away from.",
	events.TypeContract:       "A definitive agreement, supply deal or licence signed, where no order value is the point.",
	events.TypePartnership:    "A partnership, collaboration or joint venture announced.",

	events.TypeAcquisition: "The company buys another company or a stake in one.",
	events.TypeMerger:      "Two companies combine into one.",
	events.TypeDemerger:    "A spin-off, split-off or carve-out of part of the business.",
	events.TypeStakeSale:   "A holder sells a stake: secondary offering, block sale, divestment.",
	events.TypeInsolvency:  "Bankruptcy, Chapter 11 or 7, receivership, liquidation, or a going-concern warning.",

	events.TypeCapex:      "Capital spending committed: a new plant, fab, facility or capacity expansion.",
	events.TypeNewPlant:   "A specific new plant or facility opening or starting production.",
	events.TypeNewProduct: "A new product, service or drug launched or unveiled.",

	events.TypeManagementChange: "A CEO, CFO, chair or other senior officer appointed, resigning or departing.",
	events.TypeAuditorChange:    "The auditor resigns or is replaced, or issues a qualified or adverse opinion.",
	events.TypeDemise:           "The death of a director or senior officer.",

	events.TypePromoterTransaction: "A controlling shareholder buys or sells. Use insider transaction for officers and directors.",
	events.TypeInsiderTransaction:  "An officer or director buys or sells shares, as in an SEC Form 4.",
	events.TypeShareholdingChange:  "A change in who owns the company: a 13D/13G, an activist stake, index inclusion.",
	events.TypePledge:              "Shares pledged or released as collateral for a loan.",

	events.TypeAllotment:    "Shares or securities allotted or issued, with no fundraise as the point.",
	events.TypeFundRaise:    "Equity capital raised: IPO, follow-on, at-the-market, private placement, convertible.",
	events.TypeDebt:         "Debt raised, refinanced or repaid: notes, bonds, credit facility, term loan.",
	events.TypeCreditRating: "A credit rating upgraded, downgraded, affirmed, or put on watch.",

	events.TypeLitigation:         "A lawsuit, verdict, settlement or arbitration involving the company.",
	events.TypeRegulatoryAction:   "A regulator acts against the company: SEC, FTC, DOJ charges, fines, consent orders.",
	events.TypeRegulatoryApproval: "A regulator approves something: FDA approval, merger clearance, licence granted.",
	events.TypeTaxAction:          "A tax assessment, dispute or demand against the company.",

	events.TypeBoardMeeting:  "Only notice of an upcoming board meeting, with no decision announced.",
	events.TypeAGM:           "The annual shareholder meeting: notice, results of votes.",
	events.TypeEGM:           "A special shareholder meeting called outside the annual one.",
	events.TypeConcall:       "Only notice of an upcoming earnings call or webcast, with nothing said yet.",
	events.TypeTradingWindow: "A trading blackout window opening or closing for insiders.",

	events.TypePriceMovement: "The story is only that the share price moved, with no cause reported.",
	events.TypeVolumeSpurt:   "The story is only that trading volume was unusual.",
	events.TypeNewsVerify:    "The company responds to a rumour or confirms or denies a media report.",

	events.TypeAnnualReport:         "An annual report or 10-K filed, with no new material fact as the point.",
	events.TypeInvestorPresentation: "An investor presentation, analyst day or conference appearance.",
	events.TypePressRelease:         "A general press release that fits no more specific type.",
	events.TypeNewspaperPublication: "A legally required public notice with no new information.",
	events.TypeAdministrative:       "Routine administrative filing: address change, form amendment, procedural notice.",

	events.TypeSectorEvent:       "News about a whole industry rather than one named company.",
	events.TypeMacroEvent:        "An economy-wide event: Fed decision, CPI, jobs report, GDP, interest rates.",
	events.TypeCommodityEvent:    "A move or decision in a commodity: oil, gas, metals, OPEC, crops.",
	events.TypeGeopoliticalEvent: "War, sanctions, tariffs, elections, or trade restrictions between countries.",
	events.TypeRegulatoryPolicy:  "A new rule or policy affecting many companies: proposed or final rules, executive orders.",
}

// importanceLevels is the importance rubric, lowest first, as Jev scores it.
// Five levels rather than eleven: Jev accepts up to ten, and the prompt this
// replaces already judged importance in bands. The expectation over these
// levels (importanceValues) recovers a fine-grained number from the
// distribution, which is better than asking for eleven hard boundaries.
var importanceLevels = []string{
	"Not a market event at all, or pure noise: commentary, a price quote page, a data listing.",
	"Procedural: a meeting notice, a routine filing, a scheduled event with nothing decided.",
	"Worth reading: a routine order, a dividend declared, a shareholding shift.",
	"Moves the stock: a results surprise, a large order, a credit downgrade, a CEO exit.",
	"Rewrites the investment case: insolvency, a regulator halting trading, an auditor resigning over irregularities.",
}

// importanceValues maps each level to the 0-10 scale the rest of the app uses.
var importanceValues = []float64{0, 2, 5, 7.5, 9.5}
