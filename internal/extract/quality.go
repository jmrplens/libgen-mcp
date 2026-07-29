package extract

import (
	"fmt"
	"strings"
	"unicode"
)

// Thresholds for the text-layer sanity check. They are deliberately far from the
// values healthy text produces: the check is only worth making when it is close
// to certain, because a false alarm on a readable page costs the caller more than
// a missed warning on a broken one.
const (
	// qualityMinRunes is the smallest sample worth judging. A heading, a caption
	// or a page of formulas is too short to measure and would trip every ratio.
	qualityMinRunes = 400
	// qualityMaxUnmapped is the share of replacement/private-use/control runes
	// above which the text is reported as unmapped glyphs.
	qualityMaxUnmapped = 0.02
	// qualityMinWords is how many measurable Latin words the vowel test needs
	// before its ratio means anything.
	qualityMinWords = 40
	// qualityMaxVowelless is the share of Latin words with no vowel above which
	// the text is reported as a scrambled encoding. No language comes close.
	qualityMaxVowelless = 0.5
)

// qualityNote reports what looks wrong with an extracted text layer, or "" when
// nothing does. A PDF whose fonts carry no usable ToUnicode map still extracts
// text — the file is not scanned, so nothing else in the pipeline objects — but
// the characters that come out are not the ones on the page: they arrive as
// replacement or private-use glyphs, or as letters shifted onto other letters.
// Both produce text a model will summarize confidently and wrongly, so they are
// worth naming before the caller reads it.
//
// The two measures are chosen to be script-agnostic. Unmapped glyphs are counted
// over everything; the vowel test looks only at Latin-script words, so Cyrillic,
// Greek and CJK pages are judged by the first measure alone rather than by a rule
// that does not apply to them.
func qualityNote(text string) string {
	runes := []rune(text)
	if len(runes) < qualityMinRunes {
		return ""
	}
	unmapped := 0
	for _, r := range runes {
		if isUnmappedGlyph(r) {
			unmapped++
		}
	}
	if share := float64(unmapped) / float64(len(runes)); share > qualityMaxUnmapped {
		return fmt.Sprintf("this file's text layer looks damaged: %.0f%% of the characters are "+
			"unmapped glyphs, so the extracted text is not what the page shows (a broken font "+
			"encoding in the file itself; another edition may be readable)", share*100)
	}
	words, vowelless := countLatinWords(runes)
	if words < qualityMinWords {
		return ""
	}
	if share := float64(vowelless) / float64(words); share > qualityMaxVowelless {
		return fmt.Sprintf("this file's text layer looks damaged: %.0f%% of the words contain no "+
			"vowel, so the extracted text is not what the page shows (a font encoding in the file "+
			"that maps glyphs onto the wrong characters; another edition may be readable)", share*100)
	}
	return ""
}

// isUnmappedGlyph reports whether r is one of the characters a font with no
// usable ToUnicode map extracts to: the replacement character, a private-use
// code point, or a non-whitespace control character.
func isUnmappedGlyph(r rune) bool {
	switch {
	case r == unicode.ReplacementChar:
		return true
	case unicode.In(r, unicode.Co): // private use
		return true
	case unicode.IsControl(r) && !unicode.IsSpace(r):
		return true
	default:
		return false
	}
}

// countLatinWords returns how many measurable Latin-script words the text holds
// and how many of them contain no vowel. A word is measurable when it is at least
// four letters long and is not an all-caps acronym, both of which a healthy text
// produces without vowels often enough to matter (HTML, DSP, IEC).
func countLatinWords(runes []rune) (words, vowelless int) {
	for field := range strings.FieldsSeq(string(runes)) {
		w := strings.Trim(field, ".,;:!?()[]{}\"'`“”‘’—–-")
		if !isMeasurableLatinWord(w) {
			continue
		}
		words++
		if !strings.ContainsAny(strings.ToLower(w), "aeiouyàáâãäåèéêëìíîïòóôõöùúûüýÿ") {
			vowelless++
		}
	}
	return words, vowelless
}

// isMeasurableLatinWord reports whether w is a Latin-script word the vowel test
// can judge: four letters or more, letters only, and not written entirely in
// capitals (an acronym).
func isMeasurableLatinWord(w string) bool {
	if len([]rune(w)) < 4 {
		return false
	}
	caps := 0
	letters := 0
	for _, r := range w {
		if !unicode.IsLetter(r) || !unicode.In(r, unicode.Latin) {
			return false
		}
		letters++
		if unicode.IsUpper(r) {
			caps++
		}
	}
	return letters > 0 && caps != letters
}
