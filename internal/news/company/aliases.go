package company

// brandAliases maps names the press actually uses onto NSE symbols.
//
// This table is not a convenience — without it a large fraction of Indian
// market coverage is unattributable, because the name a newspaper prints is
// frequently not the name on the listing:
//
//	brand ≠ legal name   "Paytm" is listed as One 97 Communications
//	renamed issuer       Zomato became Eternal Limited; headlines still say Zomato
//	demerger             Tata Motors is now two listings, TMCV and TMPV
//	house abbreviation   "L&T", "M&M", "RIL", "HUL" appear unexplained
//
// Keys are matched after Normalize, so write them the way a person would.
var brandAliases = map[string]string{
	// Renamed issuers. The old name outlives the rename in the press by years.
	"zomato":                     "ETERNAL",
	"blinkit":                    "ETERNAL",
	"one 97 communications":      "PAYTM",
	"paytm":                      "PAYTM",
	"fsn e commerce ventures":    "NYKAA",
	"nykaa":                      "NYKAA",
	"indiabulls housing finance": "SAMMAANCAP",
	"sammaan capital":            "SAMMAANCAP",

	// House abbreviations.
	"ril":                   "RELIANCE",
	"reliance industries":   "RELIANCE",
	"l and t":               "LT",
	"larsen and toubro":     "LT",
	"m and m":               "M&M",
	"mahindra and mahindra": "M&M",
	"hul":                   "HINDUNILVR",
	"hindustan unilever":    "HINDUNILVR",
	"tcs":                   "TCS",
	"tata consultancy":      "TCS",
	"infy":                  "INFY",
	"sbi":                   "SBIN",
	"state bank of india":   "SBIN",
	"hdfc bank":             "HDFCBANK",
	"icici bank":            "ICICIBANK",
	"axis bank":             "AXISBANK",
	"kotak bank":            "KOTAKBANK",
	"kotak mahindra bank":   "KOTAKBANK",
	"bob":                   "BANKBARODA",
	"bank of baroda":        "BANKBARODA",
	"pnb":                   "PNB",
	"punjab national bank":  "PNB",

	// Acronym-named public sector undertakings. Their legal names do not
	// contain the acronym everyone uses.
	"sail":                       "SAIL",
	"steel authority of india":   "SAIL",
	"bhel":                       "BHEL",
	"bharat heavy electricals":   "BHEL",
	"ongc":                       "ONGC",
	"oil and natural gas":        "ONGC",
	"iocl":                       "IOC",
	"indian oil":                 "IOC",
	"bpcl":                       "BPCL",
	"hpcl":                       "HINDPETRO",
	"hindustan petroleum":        "HINDPETRO",
	"ntpc":                       "NTPC",
	"nhpc":                       "NHPC",
	"bel":                        "BEL",
	"bharat electronics":         "BEL",
	"hal":                        "HAL",
	"hindustan aeronautics":      "HAL",
	"bdl":                        "BDL",
	"irctc":                      "IRCTC",
	"lic":                        "LICI",
	"life insurance corporation": "LICI",
	"coal india":                 "COALINDIA",
	"gail":                       "GAIL",
	"powergrid":                  "POWERGRID",
	"power grid":                 "POWERGRID",

	// Common brand shorthands.
	"maruti":                 "MARUTI",
	"maruti suzuki":          "MARUTI",
	"airtel":                 "BHARTIARTL",
	"bharti airtel":          "BHARTIARTL",
	"vi":                     "IDEA",
	"vodafone idea":          "IDEA",
	"jio financial":          "JIOFIN",
	"dmart":                  "DMART",
	"avenue supermarts":      "DMART",
	"asian paints":           "ASIANPAINT",
	"dr reddys":              "DRREDDY",
	"dr reddys laboratories": "DRREDDY",
	"sun pharma":             "SUNPHARMA",
	"ultratech":              "ULTRACEMCO",
	"bajaj finance":          "BAJFINANCE",
	"bajaj finserv":          "BAJAJFINSV",
	"hero motocorp":          "HEROMOTOCO",
	"eicher":                 "EICHERMOT",
	"tech mahindra":          "TECHM",
	"hcl tech":               "HCLTECH",
	"hcl technologies":       "HCLTECH",
	"shriram finance":        "SHRIRAMFIN",
	"apollo hospitals":       "APOLLOHOSP",
	"tata consumer":          "TATACONSUM",
	"tata steel":             "TATASTEEL",
	"tata power":             "TATAPOWER",
	"tata elxsi":             "TATAELXSI",
	"titan":                  "TITAN",
	"trent":                  "TRENT",
	"nestle india":           "NESTLEIND",
	"britannia":              "BRITANNIA",
	"pidilite":               "PIDILITIND",
	"varun beverages":        "VBL",
	"adani enterprises":      "ADANIENT",
	"adani ports":            "ADANIPORTS",
	"adani green":            "ADANIGREEN",
	"adani power":            "ADANIPOWER",
	"jsw steel":              "JSWSTEEL",
	"vedanta":                "VEDL",
	"hindalco":               "HINDALCO",
	"grasim":                 "GRASIM",
	"siemens india":          "SIEMENS",
	"swiggy":                 "SWIGGY",
}

// tickerBlocklist names symbols that must never match as a bare word, because
// the word is ordinary English and the resulting false positives would be
// constant. These companies remain reachable by name and by explicit ticker
// notation such as "NSE: IDEA".
//
// GAIL and SAIL are deliberately absent: both are near-universally written in
// capitals as the company's actual name, and uppercase matching already keeps
// them from firing on lowercase prose.
var tickerBlocklist = map[string]bool{
	"CLEAN": true, "CROWN": true, "DEEP": true, "DOLLAR": true,
	"FACT": true, "FOCUS": true, "GLOBAL": true, "IDEA": true,
	"RAIN": true, "STAR": true, "TOTAL": true, "VITAL": true,
	// Two-letter symbols carry almost no information in running text.
	"BI": true, "TI": true, "NH": true, "LT": true,

	// US tickers that are also ordinary words or standard financial
	// abbreviations. Every one of these is a real listing, and every one
	// appears constantly in market copy meaning something else: "Dallas
	// MSA" is a metropolitan statistical area, not MSA Safety; "ALL",
	// "ANY", "CAN", "HAS", "NOW", "OUT", "WAY" are English; "AGM", "COO",
	// "CTO", "IRS", "USA", "CET" are abbreviations. Computed by
	// intersecting the SEC ticker file with the abbreviations and common
	// words that turn up in financial prose, so the list is the real
	// collision set rather than a guess.
	"AGM": true, "AGO": true, "ALL": true, "ANY": true, "ARE": true,
	"BOE": true, "CAN": true, "CAR": true, "CET": true, "COO": true,
	"CTO": true, "HAS": true, "INR": true, "IRS": true, "KEY": true,
	"LOW": true, "MSA": true, "NOW": true, "OUT": true, "PAY": true,
	"RUN": true, "TOP": true, "USA": true, "WAY": true,
}

// nameBlocklist names single-word company names that are also common English
// words. A single-token name is matched case-insensitively, so without this
// "Delta" in any context would attribute news to Delta Corp.
// A second category belongs here: a word that names a business *group* with
// several distinct listed and unlisted entities. "Kotak" alone cannot choose
// between the bank, the AMC and the securities arm, and "Embassy" cannot
// choose between Embassy Developments and the separately listed Embassy
// Office Parks REIT.
var nameBlocklist = map[string]bool{
	"kotak": true, "embassy": true, "adani": true, "birla": true,
	"godrej": true, "jindal": true, "mahindra": true, "murugappa": true,
	"delta": true, "swan": true, "orbit": true, "force": true,
	"eagle": true, "tiger": true, "lotus": true, "prime": true,
	"vision": true, "empire": true, "advance": true, "premier": true,
	"summit": true, "compass": true, "pioneer": true, "atlas": true,
}
