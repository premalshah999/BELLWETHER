package algo

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ValidationError names the exact field that is wrong, so the builder UI can
// show the message inline next to the offending input rather than as a generic
// "invalid algorithm".
type ValidationError struct {
	// Field is a JSON path such as "all[1].compare.period". Empty for errors
	// about the document as a whole.
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// ValidationErrors is the full set of problems found in one pass, so an
// operator fixes everything at once instead of one error per save.
type ValidationErrors []*ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no validation errors"
	}
	parts := make([]string, len(e))
	for i, err := range e {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "; ")
}

// Len reports how many problems were found.
func (e ValidationErrors) Len() int { return len(e) }

// friendlyJSONError turns encoding/json's terse messages into something an
// operator editing raw JSON can act on.
func friendlyJSONError(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errorsAs(err, &syntaxErr):
		return fmt.Sprintf("malformed JSON at byte %d: %s", syntaxErr.Offset, syntaxErr.Error())
	case errorsAs(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			field = "the document"
		}
		return fmt.Sprintf("%s should be %s, got %s", field, typeErr.Type, typeErr.Value)
	case err == io.EOF:
		return "the algorithm is empty"
	}
	msg := err.Error()
	// json's unknown-field message is already clear; surface it as-is.
	return msg
}

// errorsAs is a tiny local errors.As so this file does not import errors just
// for two type assertions.
func errorsAs[T error](err error, target *T) bool {
	if v, ok := err.(T); ok {
		*target = v
		return true
	}
	return false
}
