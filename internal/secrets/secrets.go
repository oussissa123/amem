// Package secrets detects credentials before they reach storage or an LLM
// (EF-063, RG-10, spec 02 §14.2).
package secrets

import (
	"math"
	"regexp"
	"strings"
)

type Rule struct {
	Name string
	Re   *regexp.Regexp
}

type Finding struct {
	Rule   string `json:"rule"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

var rules = []Rule{
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{50,}\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"stripe-key", regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{20,}\b`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{32,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"amem-token", regexp.MustCompile(`\bamem_[A-Za-z0-9]{32}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"private-key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
	{"connection-string-password", regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]{3,}@`)},
	{"assigned-secret", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret)\b\s*[:=]\s*["']?([^\s"']{8,})`)},
}

var candidateRe = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)

// allowlist removes common high-entropy non-secrets: hashes, ULIDs, UUIDs, amem keys.
var allowRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64}|[0-9A-HJKMNP-TV-Z]{26}|[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12})$`)

// Scanner applies the built-in rules plus project deny patterns.
type Scanner struct {
	extra []Rule
}

func NewScanner(denyPatterns []string) (*Scanner, error) {
	s := &Scanner{}
	for _, p := range denyPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		s.extra = append(s.extra, Rule{"project-deny-pattern", re})
	}
	return s, nil
}

func (s *Scanner) all() []Rule { return append(append([]Rule{}, rules...), s.extra...) }

// Scan reports findings without ever returning the secret value.
func (s *Scanner) Scan(text string) []Finding {
	var out []Finding
	for _, r := range s.all() {
		for _, loc := range r.Re.FindAllStringIndex(text, -1) {
			out = append(out, position(text, r.Name, loc[0]))
		}
	}
	for _, loc := range candidateRe.FindAllStringIndex(text, -1) {
		tok := text[loc[0]:loc[1]]
		if isHighEntropy(tok) && !covered(out, text, loc[0]) {
			out = append(out, position(text, "high-entropy-string", loc[0]))
		}
	}
	return out
}

// Redact replaces every finding with [REDACTED:<rule>].
func (s *Scanner) Redact(text string) (string, int) {
	n := 0
	for _, r := range s.all() {
		text = r.Re.ReplaceAllStringFunc(text, func(string) string { n++; return "[REDACTED:" + r.Name + "]" })
	}
	text = candidateRe.ReplaceAllStringFunc(text, func(tok string) string {
		if isHighEntropy(tok) {
			n++
			return "[REDACTED:high-entropy-string]"
		}
		return tok
	})
	return text, n
}

func isHighEntropy(tok string) bool {
	if allowRe.MatchString(tok) || strings.HasPrefix(tok, "REDACTED") {
		return false
	}
	// Require a mix of character classes: identifiers like
	// "connection_refused_while_starting" are not secrets.
	var lower, upper, digit bool
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	if !(digit && (lower || upper)) || !(lower && upper || digit && len(tok) >= 32) {
		return false
	}
	return entropy(tok) >= 4.0
}

func entropy(s string) float64 {
	freq := map[rune]float64{}
	for _, r := range s {
		freq[r]++
	}
	n := float64(len([]rune(s)))
	h := 0.0
	for _, f := range freq {
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}

func covered(fs []Finding, text string, offset int) bool {
	p := position(text, "", offset)
	for _, f := range fs {
		if f.Line == p.Line && f.Column <= p.Column && p.Column-f.Column < 80 {
			return true
		}
	}
	return false
}

func position(text, rule string, offset int) Finding {
	line := strings.Count(text[:offset], "\n") + 1
	col := offset - strings.LastIndex(text[:offset], "\n")
	return Finding{Rule: rule, Line: line, Column: col}
}
