package events

import "strings"

// parsePipeFacts reads the "KEY: value | KEY: value" shape the ingestion
// engine writes for a feed's structured fields (the Federal Register's
// agency and document type) into a map with upper-cased keys.
func parsePipeFacts(desc string) map[string]string {
	facts := map[string]string{}
	for _, part := range strings.Split(desc, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		facts[key] = value
	}
	return facts
}
