package sanitize

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// Text strips HTML tags from s, collapses runs of whitespace to a single
// space, and trims the result. It is applied to every user-supplied string
// before it leaves the server so that the browser never receives markup
// regardless of how the frontend renders the value.
func Text(s string) string {
	// Reject inputs that are unreasonably long before doing any regex work.
	if utf8.RuneCountInString(s) > 64_000 {
		s = string([]rune(s)[:64_000])
	}
	s = htmlTag.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return s
}
