// Package media discovers, parses, and probes media files on disk.
package media

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ParsedName is what a filename yielded.
//
// Year is nil when no plausible year was found. That is an accepted outcome:
// 0.0.1's parsing is deliberately minimal and a bad parse is acceptable — the
// media only has to appear and play.
type ParsedName struct {
	Title string
	Year  *int
}

// yearPattern matches a four-digit year as its own token, optionally wrapped
// in parentheses, brackets, or braces.
//
// The bounds matter: without a lower bound, "1080" from a resolution would
// parse as a year, and the upper bound keeps a stray number in a title from
// being mistaken for one.
// delimitedYear is a year wrapped in parentheses, brackets, or braces. The
// punctuation is deliberate, so it outranks a bare number: "Blade Runner 2049
// (2017)" is a 2017 film, not a 2049 one.
var delimitedYear = regexp.MustCompile(`^[\(\[\{]((?:19|20)\d{2})[\)\]\}]$`)

// bareYear is a year standing alone as a token, the form scene names use.
var bareYear = regexp.MustCompile(`^((?:19|20)\d{2})$`)

// ParseName extracts a title and year from a filename or directory name.
//
// The MVP describes the common form Title (Year). Scene releases use dots
// instead of spaces — Idiocracy.2006.1080p.DSNP.WEB-DL... — and are common
// enough in a real library that treating a dot as a separator is worth the few
// extra lines. Everything before the first year token becomes the title.
//
// This is not a release-name parser and must not grow into one. It does not
// interpret editions, resolutions, source tags, codecs, or release groups; it
// finds a year and takes what precedes it.
func ParseName(name string) ParsedName {
	base := stripExtension(name)

	// Scene names separate words with dots or underscores. A name that already
	// uses spaces is left alone, so "The Legend of Aang - The Last Airbender"
	// keeps its punctuation.
	normalised := base
	if !strings.Contains(base, " ") {
		normalised = strings.NewReplacer(".", " ", "_", " ").Replace(base)
	}

	fields := strings.Fields(normalised)

	// A delimited year wins wherever it appears, because the punctuation is a
	// deliberate signal. Only when there is none does a bare token count.
	if parsed, ok := findYear(fields, delimitedYear); ok {
		return parsed
	}
	if parsed, ok := findYear(fields, bareYear); ok {
		return parsed
	}

	// No year found. Return the whole cleaned name as the title rather than
	// nothing — an ugly title is recoverable, a missing one is not.
	title := cleanTitle(normalised)
	if title == "" {
		title = base
	}
	if title == "" {
		title = name
	}
	return ParsedName{Title: title}
}

// findYear returns the first field matching pattern, with everything before it
// as the title.
//
// A match in the first position is ignored: it leaves no title behind it, so
// it is far more likely to be the title itself ("2012", "1917").
func findYear(fields []string, pattern *regexp.Regexp) (ParsedName, bool) {
	for i, f := range fields {
		if i == 0 {
			continue
		}

		m := pattern.FindStringSubmatch(f)
		if m == nil {
			continue
		}

		year, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}

		title := cleanTitle(strings.Join(fields[:i], " "))
		if title == "" {
			continue
		}
		return ParsedName{Title: title, Year: &year}, true
	}
	return ParsedName{}, false
}

// stripExtension removes a trailing file extension, leaving directory names
// containing dots intact.
func stripExtension(name string) string {
	ext := filepath.Ext(name)
	if ext == "" || len(ext) > 5 {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// cleanTitle tidies the text preceding the year.
//
// It trims separator punctuation from the ends and collapses whitespace. It
// does not attempt to fix capitalisation or expand abbreviations.
func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("-_.[](){}", r)
	})
	return strings.TrimSpace(s)
}
