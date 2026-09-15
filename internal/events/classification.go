package events

import (
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// EntityReading is the model's judgement about one company's exposure to an
// event.
type EntityReading struct {
	Symbol         string
	Relationship   news.Relationship
	Direction      news.Direction
	ImpactStrength float64
	Rationale      string
}

// Classification is what the model returns for one event.
//
// It is a separate type from news.Event rather than fields written directly
// onto it, so that a classification can be stored, replaced, or discarded
// without touching the deterministic facts underneath. If the model is wrong,
// only this is wrong, and it can be recomputed from evidence that was never
// overwritten.
type Classification struct {
	EventID      int64
	Type         string
	Importance   *int
	Confidence   *float64
	Summary      string
	WhyItMatters string
	Entities     []EntityReading
	Model        string
	ClassifiedAt time.Time
}

// allTypes is every type the classifier may return. It is derived from one
// list so that adding a type cannot leave the prompt and the validator
// disagreeing about what is legal.
var allTypes = []Type{
	TypeEarnings, TypeGuidance,
	TypeDividend, TypeBonus, TypeStockSplit, TypeBuyback, TypeRightsIssue, TypeRecordDate,
	TypeOrderWin, TypeOrderCancelled, TypeContract, TypePartnership,
	TypeAcquisition, TypeMerger, TypeDemerger, TypeStakeSale, TypeInsolvency,
	TypeCapex, TypeNewPlant, TypeNewProduct,
	TypeManagementChange, TypeAuditorChange, TypeDemise,
	TypePromoterTransaction, TypeInsiderTransaction, TypeShareholdingChange,
	TypePledge, TypeAllotment, TypeFundRaise, TypeDebt, TypeCreditRating,
	TypeLitigation, TypeRegulatoryAction, TypeRegulatoryApproval, TypeTaxAction,
	TypeBoardMeeting, TypeAGM, TypeEGM, TypeConcall, TypeTradingWindow,
	TypePriceMovement, TypeVolumeSpurt, TypeNewsVerify,
	TypeAnnualReport, TypeInvestorPresentation, TypePressRelease,
	TypeNewspaperPublication, TypeAdministrative,
	TypeSectorEvent, TypeMacroEvent, TypeCommodityEvent, TypeGeopoliticalEvent,
	TypeRegulatoryPolicy,
	TypeUnclassified,
}

var typeSet = func() map[Type]bool {
	m := make(map[Type]bool, len(allTypes))
	for _, t := range allTypes {
		m[t] = true
	}
	// FUND_NAV is knowable but is never a valid classifier output: it is
	// decided deterministically from the exchange's own subject line, and
	// letting the model assign it would give it a way to hide equity news.
	return m
}()

// KnownType reports whether a type is one the classifier may assign.
func KnownType(t Type) bool { return typeSet[t] }

// AllTypeNames lists the assignable types, for the prompt.
func AllTypeNames() []string {
	out := make([]string, 0, len(allTypes))
	for _, t := range allTypes {
		if t == TypeUnclassified {
			continue
		}
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}
