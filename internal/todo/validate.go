package todo

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// validateTitle normalises and checks a title. It returns the trimmed title
// and an empty reason when valid, or a reason the client can show verbatim.
func validateTitle(raw string) (title, reason string) {
	title = strings.TrimSpace(raw)
	// utf8.RuneCountInString, never len: len counts bytes, and "é" is two of them.
	switch n := utf8.RuneCountInString(title); {
	case n == 0:
		return "", "must not be blank"
	case n > MaxTitleRunes:
		return "", fmt.Sprintf("must be at most %d characters", MaxTitleRunes)
	}
	for _, r := range title { // ranging over a string yields runes, not bytes
		if unicode.IsControl(r) {
			return "", "must not contain control characters"
		}
	}
	// TrimSpace strips White_Space and IsControl covers the Cc category, but
	// neither catches format characters such as a zero-width space (U+200B) or
	// a byte-order mark: a title must show something. Do not reject all format
	// characters, though: the zero-width joiner (U+200D) is one, and it is what
	// holds emoji sequences together.
	if !strings.ContainsFunc(title, func(r rune) bool { return unicode.IsGraphic(r) && !unicode.IsSpace(r) }) {
		return "", "must contain at least one visible character"
	}
	return title, ""
}
