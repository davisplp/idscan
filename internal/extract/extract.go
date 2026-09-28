// Package extract finds the phone number field within OCR'd ID card text
// and normalizes it to a canonical Kenyan E.164 format.
package extract

import (
	"fmt"
	"regexp"
	"strings"
)

// candidateDigits matches runs of 9-12 digits, which comfortably covers a
// Kenyan number in local (10-digit), bare (9-digit), or international
// (12-digit, with or without a leading +) form, even once OCR has dropped
// spaces/dashes or a plus sign.
var candidateDigits = regexp.MustCompile(`\d{9,12}`)

// labelPattern matches the "Contact No" field label loosely: case
// insensitive, tolerant of a missing/extra period, and tolerant of "No"
// vs "No." vs "Number". This is deliberately loose because OCR frequently
// mangles small label text worse than it mangles the larger printed digits
// next to it.
var labelPattern = regexp.MustCompile(`(?i)contact\s*(no|number)\b`)

// cardLabelPattern matches the card's own identifier field. Card templates
// seen in practice use either "Card No." (e.g. "PLP-26-YOM-001-S9Z") or
// "Identification No." (e.g. a plain numeric ID) for what is functionally
// the same field: the card's unique identifier. Both are treated as
// equivalent here, the same way ContactNumber's label matching tolerates
// wording variation across templates.
var cardLabelPattern = regexp.MustCompile(`(?i)(card\s*no|identification\s*no)\b`)

// dashedCodePattern matches a multi-segment dash-separated code such as
// "PLP-26-YOM-001-S9Z" -- generically, not hardcoded to the "PLP" prefix,
// so a future program/card-code scheme still matches. Requires at least
// three dash-separated segments so it doesn't accidentally match an
// ordinary hyphenated word or a date range like "2026 - 2027".
var dashedCodePattern = regexp.MustCompile(`[A-Za-z0-9]+(?:-[A-Za-z0-9]+){2,}`)

// longDigitRun is the fallback for card templates that print a plain
// numeric identifier instead of a dashed code (seen in practice on the
// "Identification No." template variant).
var longDigitRun = regexp.MustCompile(`\d{6,}`)

// CardResult carries the card's identifier as read from the OCR text.
// Unlike a phone number, a card number has no normalization step: it's
// used as an exact, case-insensitive match key against students.card_number.
type CardResult struct {
	RawCardNumber string // as read from the card, uppercased
	SourceLine    string // the OCR line the match came from, for debugging/UI display
}

// CardNumber searches OCR'd text for the ID card's own identifier field
// ("Card No." or "Identification No."), using the same label-anchored,
// line-scoped approach as ContactNumber -- for the same reason: scanning
// the whole card for anything code-shaped risks picking up a fragment of
// an unrelated field.
func CardNumber(ocrText string) (CardResult, error) {
	lines := strings.Split(ocrText, "\n")

	for i, line := range lines {
		if !cardLabelPattern.MatchString(line) {
			continue
		}

		if res, ok := tryCardLine(line); ok {
			return res, nil
		}
		if i+1 < len(lines) {
			if res, ok := tryCardLine(lines[i+1]); ok {
				return res, nil
			}
		}
	}

	return CardResult{}, fmt.Errorf("extract: no Card No. field found in OCR text")
}

func tryCardLine(line string) (CardResult, bool) {
	if m := dashedCodePattern.FindString(line); m != "" {
		return CardResult{RawCardNumber: strings.ToUpper(m), SourceLine: strings.TrimSpace(line)}, true
	}
	if m := longDigitRun.FindString(line); m != "" {
		return CardResult{RawCardNumber: m, SourceLine: strings.TrimSpace(line)}, true
	}
	return CardResult{}, false
}

// ExtractResult carries both the raw digits we found and what we could
// normalize them to, plus enough context for a human reviewer to judge the
// extraction if normalization failed or confidence is low.
type ExtractResult struct {
	RawMatch   string // the raw digit run as read from the OCR text
	Normalized string // E.164 form, e.g. "+254740133967"
	SourceLine string // the OCR line the match came from, for debugging/UI display
}

// ContactNumber searches OCR'd text for the ID card's "Contact No." field
// and returns the normalized phone number. It anchors on the field label
// rather than scanning the whole card for anything digit-shaped, because
// card numbers, ID numbers, and QR-adjacent text can otherwise produce
// false positives (see internal/extract/extract_test.go).
func ContactNumber(ocrText string) (ExtractResult, error) {
	lines := strings.Split(ocrText, "\n")

	for i, line := range lines {
		if !labelPattern.MatchString(line) {
			continue
		}

		// Try the label's own line first (label and value are usually on
		// the same line, separated by a colon).
		if res, ok := tryLine(line); ok {
			return res, nil
		}

		// Some OCR passes split the label and value onto separate lines
		// (e.g. if the colon renders oddly). Check the next line too.
		if i+1 < len(lines) {
			if res, ok := tryLine(lines[i+1]); ok {
				return res, nil
			}
		}
	}

	return ExtractResult{}, fmt.Errorf("extract: no Contact No. field found in OCR text")
}

func tryLine(line string) (ExtractResult, bool) {
	match := candidateDigits.FindString(line)
	if match == "" {
		return ExtractResult{}, false
	}
	normalized, err := NormalizeKenyanPhone(match)
	if err != nil {
		// Found digits near the label but they don't look like a phone
		// number (e.g. a stray card-number fragment). Report the raw match
		// via the error path's caller so it still surfaces for manual review
		// rather than silently disappearing.
		return ExtractResult{RawMatch: match, SourceLine: strings.TrimSpace(line)}, false
	}
	return ExtractResult{RawMatch: match, Normalized: normalized, SourceLine: strings.TrimSpace(line)}, true
}

var nonDigit = regexp.MustCompile(`\D`)

// NormalizeKenyanPhone converts a raw digit string (already stripped of
// most non-digits by the caller, but we strip again defensively) into
// E.164 format, e.g. "0740133967" -> "+254740133967".
//
// It also applies a loose validity check on the 9-digit subscriber number:
// legitimate Safaricom/Airtel/Telkom mobile numbers start with 1 or 7
// (covering the 07x and 01x ranges); a landline exception for 020-style
// numbers is deliberately NOT handled here since this tool targets mobile
// contact numbers on ID cards.
func NormalizeKenyanPhone(raw string) (string, error) {
	digits := nonDigit.ReplaceAllString(raw, "")

	var body string // the 9-digit subscriber number, no leading 0/254

	switch {
	case strings.HasPrefix(digits, "254") && len(digits) == 12:
		body = digits[3:]
	case strings.HasPrefix(digits, "0") && len(digits) == 10:
		body = digits[1:]
	case len(digits) == 9:
		body = digits
	default:
		return "", fmt.Errorf("extract: %q is not a recognizable Kenyan phone number length", raw)
	}

	if body[0] != '1' && body[0] != '7' {
		return "", fmt.Errorf("extract: %q does not match a Kenyan mobile prefix (07x/01x)", raw)
	}

	return "+254" + body, nil
}
