package company

// brandAliases map names the press uses onto tickers, where the name printed
// is not the registered one: a brand ("Google" is Alphabet), an abbreviation
// ("J&J", "TSMC"), or a legal name that never appears as written ("SCHWAB
// CHARLES CORP"). Keys are matched after Normalize; an alias naming a ticker
// that is not listed is skipped.
var brandAliases = map[string]string{
	"google":               "GOOGL",
	"facebook":             "META",
	"meta":                 "META",
	"amazon":               "AMZN",
	"jpmorgan":             "JPM",
	"jp morgan":            "JPM",
	"goldman sachs":        "GS",
	"goldman":              "GS",
	"bofa":                 "BAC",
	"citi":                 "C",
	"citibank":             "C",
	"berkshire":            "BRK-B",
	"schwab":               "SCHW",
	"charles schwab":       "SCHW",
	"exxon":                "XOM",
	"exxon mobil":          "XOM",
	"exxonmobil":           "XOM",
	"disney":               "DIS",
	"mcdonald":             "MCD", // "McDonald's" once the possessive is stripped
	"paypal":               "PYPL",
	"uber":                 "UBER",
	"ford":                 "F",
	"verizon":              "VZ",
	"t mobile":             "TMUS",
	"costco":               "COST",
	"lilly":                "LLY",
	"j and j":              "JNJ",
	"unitedhealth":         "UNH",
	"ibm":                  "IBM",
	"pepsi":                "PEP",
	"p and g":              "PG",
	"ge aerospace":         "GE",
	"alibaba":              "BABA",
	"tsmc":                 "TSM",
	"taiwan semiconductor": "TSM",
	// The short form the press prints of a longer registered name.
	"philip morris":    "PM",
	"raymond james":    "RJF",
	"martin marietta":  "MLM",
	"fifth third":      "FITB",
	"super micro":      "SMCI",
	"supermicro":       "SMCI",
	"bristol myers":    "BMY",
	"alaska airlines":  "ALK",
	"alaska air":       "ALK",
	"under armour":     "UAA",
	"dutch bros":       "BROS",
	"norwegian cruise": "NCLH",
	"carrier global":   "CARR",
}

// tickerBlocklist names tickers that are ordinary words or standard
// abbreviations in market copy -- "Dallas MSA" is a metropolitan area, not
// MSA Safety; "ALL", "NOW", "CAN" are English; "COO", "IRS", "AGM" are
// abbreviations -- so they never match as a bare word. Computed from the SEC
// ticker file against the words that turn up in financial prose.
var tickerBlocklist = map[string]bool{
	"AGM": true, "AGO": true, "ALL": true, "ANY": true, "ARE": true,
	"BOE": true, "CAN": true, "CAR": true, "CET": true, "COO": true,
	"CTO": true, "HAS": true, "INR": true, "IRS": true, "KEY": true,
	"LOW": true, "MSA": true, "NOW": true, "OUT": true, "PAY": true,
	"RUN": true, "TOP": true, "USA": true, "WAY": true,
	"CLEAN": true, "CROWN": true, "DEEP": true, "DOLLAR": true, "FACT": true,
	"FOCUS": true, "GLOBAL": true, "IDEA": true, "RAIN": true, "STAR": true,
	"TOTAL": true, "VITAL": true,
}
