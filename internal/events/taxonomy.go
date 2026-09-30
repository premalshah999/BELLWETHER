// Package events turns raw ingested items into classified market events.
//
// It is the layer between collection and intelligence: news.Engine gathers
// bytes, this package decides what happened, to whom, and how much it matters.
// Everything here that can be done deterministically is done deterministically,
// because an exchange filing states its own category and there is no sense
// paying a model to guess at something the document already says.
package events

// Type is the classification of an event.
//
// The taxonomy is deliberately finer than "business news". An operator cannot
// act on a category; they can act on knowing that a company won an order, that
// its auditor resigned, or that the exchange has asked it to explain a price
// move.
type Type string

const (
	TypeUnclassified Type = "UNCLASSIFIED"

	// Results and guidance.
	TypeEarnings Type = "EARNINGS"
	TypeGuidance Type = "GUIDANCE"

	// Returns to shareholders.
	TypeDividend    Type = "DIVIDEND"
	TypeBonus       Type = "BONUS"
	TypeStockSplit  Type = "STOCK_SPLIT"
	TypeBuyback     Type = "BUYBACK"
	TypeRightsIssue Type = "RIGHTS_ISSUE"
	TypeRecordDate  Type = "RECORD_DATE"

	// Commercial activity.
	TypeOrderWin       Type = "ORDER_WIN"
	TypeOrderCancelled Type = "ORDER_CANCELLED"
	TypeContract       Type = "CONTRACT"
	TypePartnership    Type = "PARTNERSHIP"

	// Structure.
	TypeAcquisition Type = "ACQUISITION"
	TypeMerger      Type = "MERGER"
	TypeDemerger    Type = "DEMERGER"
	TypeStakeSale   Type = "STAKE_SALE"
	TypeInsolvency  Type = "INSOLVENCY"

	// Investment.
	TypeCapex      Type = "CAPEX"
	TypeNewPlant   Type = "NEW_PLANT"
	TypeNewProduct Type = "NEW_PRODUCT"

	// People.
	TypeManagementChange Type = "MANAGEMENT_CHANGE"
	TypeAuditorChange    Type = "AUDITOR_CHANGE"
	TypeDemise           Type = "DEMISE"

	// Ownership and money flows.
	TypePromoterTransaction Type = "PROMOTER_TRANSACTION"
	TypeInsiderTransaction  Type = "INSIDER_TRANSACTION"
	TypeShareholdingChange  Type = "SHAREHOLDING_CHANGE"
	TypePledge              Type = "PLEDGE"
	TypeAllotment           Type = "ALLOTMENT"
	TypeFundRaise           Type = "FUND_RAISE"
	TypeDebt                Type = "DEBT"
	TypeCreditRating        Type = "CREDIT_RATING"

	// Trouble.
	TypeLitigation       Type = "LITIGATION"
	TypeRegulatoryAction Type = "REGULATORY_ACTION"
	// TypeRegulatoryApproval is the opposite sign of TypeRegulatoryAction and
	// must not share a type with it. A licence granted and a licence
	// suspended arrive through the same NSE subject line, and collapsing them
	// would hand the model an event whose direction is already wrong.
	TypeRegulatoryApproval Type = "REGULATORY_APPROVAL"
	TypeTaxAction          Type = "TAX_ACTION"

	// Governance calendar.
	TypeBoardMeeting  Type = "BOARD_MEETING"
	TypeAGM           Type = "AGM"
	TypeEGM           Type = "EGM"
	TypeConcall       Type = "CONCALL"
	TypeTradingWindow Type = "TRADING_WINDOW"

	// Exchange surveillance. The exchange asking a company to explain a move
	// is a real signal and deserves its own type rather than being filed as
	// an administrative update.
	TypePriceMovement Type = "PRICE_MOVEMENT"
	TypeVolumeSpurt   Type = "VOLUME_SPURT"
	TypeNewsVerify    Type = "NEWS_VERIFICATION"

	// Disclosure that rarely moves anything on its own.
	TypeAnnualReport         Type = "ANNUAL_REPORT"
	TypeInvestorPresentation Type = "INVESTOR_PRESENTATION"
	TypePressRelease         Type = "PRESS_RELEASE"
	TypeNewspaperPublication Type = "NEWSPAPER_PUBLICATION"
	TypeAdministrative       Type = "ADMINISTRATIVE"

	// Above the company.
	TypeSectorEvent       Type = "SECTOR_EVENT"
	TypeMacroEvent        Type = "MACRO_EVENT"
	TypeCommodityEvent    Type = "COMMODITY_EVENT"
	TypeGeopoliticalEvent Type = "GEOPOLITICAL_EVENT"
	TypeRegulatoryPolicy  Type = "REGULATORY_POLICY"

	// TypeFundNAV is a mutual-fund net asset value declaration. It is
	// classified rather than discarded so the filter is visible and
	// reversible, but it is not equity news: these alone are 440 of the 1,600
	// items in a day's NSE announcements, and letting them through would mean
	// a feed that is one quarter signal.
	TypeFundNAV Type = "FUND_NAV"
)

// Equity reports whether this type belongs in an equity operator's feed.
//
// The judgement is about the instrument, not about importance. A fund's NAV
// declaration is a perfectly valid disclosure that simply concerns a different
// asset class from the one this application is about.
func (t Type) Equity() bool { return t != TypeFundNAV }

// BaselineImportance is the prior for a type, on the 0-10 scale, before any
// model looks at the specific event.
//
// It exists so that the system is useful before the AI layer runs at all, and
// so that the model has something to disagree with rather than a blank page. A
// regulatory action against a company matters more than a newspaper
// publication notice no matter what the text says, and that ordering is
// knowable from the category alone.
func (t Type) BaselineImportance() int {
	switch t {
	case TypeRegulatoryAction, TypeInsolvency, TypeTaxAction:
		return 9
	case TypeEarnings, TypeAcquisition, TypeMerger, TypeDemerger, TypeAuditorChange:
		return 8
	case TypeGuidance, TypeOrderWin, TypeCreditRating, TypeLitigation,
		TypeStakeSale, TypeBuyback, TypePledge, TypeRegulatoryApproval:
		return 7
	// A management change spans a CFO resigning unexpectedly and a routine
	// change of registrar, and the subject line does not distinguish them.
	// The baseline sits between the two and leaves the judgement to the
	// classifier, which can read the text.
	case TypeManagementChange:
		return 6
	case TypeDividend, TypeBonus, TypeStockSplit, TypeRightsIssue, TypeFundRaise,
		TypeContract, TypePartnership, TypeCapex, TypeNewPlant, TypeDebt,
		TypePriceMovement, TypeNewsVerify, TypeOrderCancelled:
		return 6
	case TypePromoterTransaction, TypeInsiderTransaction, TypeShareholdingChange,
		TypeNewProduct, TypeVolumeSpurt, TypeDemise:
		return 5
	case TypeBoardMeeting, TypeConcall, TypeAllotment, TypeRecordDate, TypePressRelease:
		return 4
	case TypeAGM, TypeEGM, TypeInvestorPresentation, TypeAnnualReport:
		return 3
	case TypeSectorEvent, TypeMacroEvent, TypeCommodityEvent, TypeGeopoliticalEvent,
		TypeRegulatoryPolicy:
		return 6
	case TypeTradingWindow, TypeNewspaperPublication, TypeAdministrative:
		return 2
	case TypeFundNAV:
		return 0
	default:
		return 3
	}
}

// Some Type constants above have no producer any more. They stay because
// archived events are read back through them.
