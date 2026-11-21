// Package chunk splits a knowledge document into search units (spec 02 §6.1).
package chunk

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/oussissa/amem/internal/domain"
)

type Chunk struct {
	Ordinal int
	Section string // "header" or the heading path
	Text    string
	// Header-only weighted parts.
	A, B string
}

const (
	targetTokens = 600
)

// Tokens approximates a token count (spec 02 §6.1: runes / 3.5, rounded up).
func Tokens(s string) int {
	n := len([]rune(s))
	return (n*2 + 6) / 7
}

var headingRe = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)

// Split returns the header chunk followed by body sections.
func Split(k *domain.Knowledge) []Chunk {
	a := strings.Join(append([]string{k.Key, k.Title, ExpandIdentifiers(strings.Join(k.Keywords, " "))},
		append(k.Components, ExpandIdentifiers(strings.Join(k.Components, " ")))...), " ")
	a += " " + strings.Join(k.Topics, " ") + " " + strings.Join(k.Technologies, " ")
	b := k.Abstract + "\n" + k.Overview
	out := []Chunk{{Ordinal: 0, Section: "header", A: a, B: b,
		Text: k.Title + "\n" + k.Abstract + "\n" + k.Overview}}

	var path [3]string
	var buf strings.Builder
	inCode := false
	flush := func() {
		text := strings.TrimSpace(buf.String())
		buf.Reset()
		if text == "" {
			return
		}
		section := strings.Trim(strings.Join(path[:], " › "), " ›")
		for _, part := range splitLong(text) {
			out = append(out, Chunk{Ordinal: len(out), Section: section, Text: part})
		}
	}
	for _, line := range strings.Split(k.Body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
		if m := headingRe.FindStringSubmatch(line); m != nil && !inCode {
			flush()
			level := len(m[1]) - 1
			path[level] = strings.TrimSpace(m[2])
			for i := level + 1; i < len(path); i++ {
				path[i] = ""
			}
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return out
}

// splitLong cuts a section at paragraph boundaries around targetTokens,
// never inside a fenced code block.
func splitLong(text string) []string {
	if Tokens(text) <= targetTokens {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	inCode := false
	for _, para := range strings.Split(text, "\n\n") {
		if strings.Count(para, "```")%2 == 1 {
			inCode = !inCode
		}
		if cur.Len() > 0 && !inCode && Tokens(cur.String()+para) > targetTokens {
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(para)
		cur.WriteString("\n\n")
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// ExpandIdentifiers splits CamelCase, snake_case and paths into words so that
// "PaymentService" also matches "payment service" (spec 02 §6.2).
func ExpandIdentifiers(s string) string {
	var words []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		var cur []rune
		rs := []rune(tok)
		for i, r := range rs {
			if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				words = append(words, string(cur))
				cur = nil
			}
			cur = append(cur, r)
		}
		words = append(words, string(cur))
	}
	return strings.ToLower(strings.Join(words, " "))
}
