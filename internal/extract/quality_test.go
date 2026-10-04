package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// englishSample is a healthy Latin-script text layer, long enough to clear the
// sampling floor.
const englishSample = `The propagation of sound in a room is governed by the ` +
	`geometry of its boundaries and by the absorption of the materials that ` +
	`cover them. Reverberation time, the interval over which the sound pressure ` +
	`level decays by sixty decibels, remains the single most quoted measure of ` +
	`room acoustics, even though it says little about the early reflections that ` +
	`shape clarity. A measurement performed with an impulse response captures ` +
	`both, and modern practice reports several parameters derived from it.`

// TestQualityNote_HealthyTextIsNotFlagged verifies that ordinary prose in a
// Latin script, in Cyrillic and in CJK produces no note: the check exists to
// catch a broken font encoding, and flagging a perfectly readable page in
// another script would be worse than saying nothing.
func TestQualityNote_HealthyTextIsNotFlagged(t *testing.T) {
	cases := map[string]string{
		"english": englishSample,
		"russian": strings.Repeat("Распространение звука в помещении определяется геометрией его границ "+
			"и поглощением материалов, которыми они покрыты. Время реверберации остаётся "+
			"самой цитируемой мерой акустики помещения. ", 3),
		"chinese": strings.Repeat("室内声音的传播取决于其边界的几何形状以及覆盖它们的材料的吸收特性。"+
			"混响时间仍然是房间声学中最常被引用的量度。", 6),
		"with acronyms": englishSample + " See the HTML, XML, PDF, DVD, MP3, LCD, DSP, FFT, " +
			"RMS, SPL, THD, IEC, ISO, DIN, ANSI, ASTM, IEEE, ACM, NTSC, PAL specifications.",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if note := qualityNote(text); note != "" {
				t.Errorf("qualityNote flagged healthy text: %q", note)
			}
		})
	}
}

// TestQualityNote_UnmappedGlyphsAreFlagged verifies that a text layer peppered
// with replacement characters and private-use glyphs — what a PDF whose font has
// no usable ToUnicode map extracts to — is reported.
func TestQualityNote_UnmappedGlyphsAreFlagged(t *testing.T) {
	text := englishSample + strings.Repeat(" ��", 40)
	note := qualityNote(text)
	if note == "" {
		t.Fatal("qualityNote returned no note for text full of unmapped glyphs")
	}
	if !strings.Contains(note, "unmapped glyphs") {
		t.Errorf("note should name what it measured, got %q", note)
	}
}

// TestQualityNote_CountsEveryKindOfUnmappedGlyph covers the three characters a
// font with no usable character map extracts to — the replacement character, a
// private-use code point and a bare control character — since a document that
// trips only the last of them is as unreadable as one that trips the first.
func TestQualityNote_CountsEveryKindOfUnmappedGlyph(t *testing.T) {
	cases := map[string]string{
		"replacement character": "�",
		"private use area":      "",
		"control character":     "\x01",
	}
	for name, glyph := range cases {
		t.Run(name, func(t *testing.T) {
			note := qualityNote(englishSample + strings.Repeat(" "+glyph, 40))
			if note == "" {
				t.Fatalf("qualityNote did not flag a text layer full of %s", name)
			}
			if !strings.Contains(note, "unmapped glyphs") {
				t.Errorf("note should name what it measured, got %q", note)
			}
		})
	}
	// Whitespace is not a control character for this purpose: a page full of line
	// breaks and tabs is ordinary layout, not a broken encoding.
	if note := qualityNote(englishSample + strings.Repeat("\n\t\r\v\f", 40)); note != "" {
		t.Errorf("qualityNote flagged ordinary whitespace: %q", note)
	}
}

// TestQualityNote_ConsonantSoupIsFlagged verifies the other failure mode: a font
// whose glyphs map to arbitrary letters extracts words that no language could
// produce, with no vowel in sight.
func TestQualityNote_ConsonantSoupIsFlagged(t *testing.T) {
	text := strings.Repeat("qwrtp lkjhg zxcvbnm ffgghh mnbvcxz trwqpl kjhgfds ", 20)
	note := qualityNote(text)
	if note == "" {
		t.Fatal("qualityNote returned no note for consonant soup")
	}
	if !strings.Contains(note, "vowel") {
		t.Errorf("note should say what looks wrong, got %q", note)
	}
}

// TestExtract_ReportsADamagedTextLayer verifies that a chunk carries the quality
// note through Extract, so a caller reading a file whose fonts extract to
// nonsense is told before it summarizes the nonsense — and that a healthy file
// carries no note.
func TestExtract_ReportsADamagedTextLayer(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.txt")
	if err := os.WriteFile(broken, []byte(strings.Repeat("qwrtp lkjhg zxcvbnm ffgghh mnbvcxz ", 20)), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Extract(context.Background(), openFile(t, broken), Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.QualityNote == "" {
		t.Errorf("QualityNote is empty for a damaged text layer: %+v", c)
	}

	healthy := filepath.Join(dir, "healthy.txt")
	if werr := os.WriteFile(healthy, []byte(englishSample), 0o600); werr != nil {
		t.Fatal(werr)
	}
	c, err = Extract(context.Background(), openFile(t, healthy), Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.QualityNote != "" {
		t.Errorf("QualityNote = %q, want empty for healthy text", c.QualityNote)
	}
}

// TestQualityNote_ShortSampleIsNotJudged verifies that a chunk too small to
// measure is never flagged: a page of formulas or a heading-only page would
// otherwise trip every threshold.
func TestQualityNote_ShortSampleIsNotJudged(t *testing.T) {
	for _, text := range []string{"", "  \n\t ", "Fig. 3.1", "���", "qwrtp lkjhg"} {
		t.Run(text, func(t *testing.T) {
			if note := qualityNote(text); note != "" {
				t.Errorf("qualityNote(%q) = %q, want no note for a sample this short", text, note)
			}
		})
	}
}

// vowelWords returns n measurable Latin words of twelve letters, the first
// vowelless of them with no vowel, separated by spaces: 13n runes of text whose
// vowel measure is exactly vowelless/n.
func vowelWords(n, vowelless int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = "qwrtpsdfghjk"
		if i >= vowelless {
			words[i] = "qwrtpsdfghja"
		}
	}
	return strings.Join(words, " ") + " "
}

// TestQualityNote_Thresholds drives each measure to its edges: the shortest
// sample judged, a share of unmapped glyphs or of vowelless words exactly at
// its threshold (not flagged, the thresholds being strict) and one past it,
// and the fewest words the vowel measure takes. Each note quotes the share it
// measured as a percentage.
func TestQualityNote_Thresholds(t *testing.T) {
	// 392 runes of measurable words that all carry a vowel, so only the glyphs
	// appended to them are measured.
	healthy := strings.Repeat("abcd ", 78) + "ab"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"the shortest sample judged", strings.Repeat("�", qualityMinRunes), "100% of the characters are unmapped glyphs"},
		{"unmapped glyphs at the threshold", healthy + strings.Repeat("�", 8), ""},
		{"one unmapped glyph", healthy + "�" + strings.Repeat("a", 7), ""},
		{"unmapped glyphs past the threshold", healthy[:391] + strings.Repeat("�", 9), "2% of the characters are unmapped glyphs"},
		{"the fewest words measured", vowelWords(qualityMinWords, qualityMinWords), "100% of the words contain no vowel"},
		{"one word too few", vowelWords(qualityMinWords-1, qualityMinWords-1) + strings.Repeat("7", 13), ""},
		{"vowelless words at the threshold", vowelWords(qualityMinWords, qualityMinWords/2), ""},
		{"a few vowelless words", vowelWords(qualityMinWords, 5), ""},
		{"vowelless words past the threshold", vowelWords(qualityMinWords, qualityMinWords/2+1), "52% of the words contain no vowel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := len([]rune(tc.text)); n < qualityMinRunes {
				t.Fatalf("the fixture is %d runes, under the %d the check judges", n, qualityMinRunes)
			}
			note := qualityNote(tc.text)
			if tc.want == "" && note != "" {
				t.Errorf("qualityNote = %q, want no note", note)
			}
			if tc.want != "" && !strings.Contains(note, tc.want) {
				t.Errorf("qualityNote = %q, want it to contain %q", note, tc.want)
			}
		})
	}
}

// TestIsUnmappedGlyph pins each kind of character a broken font extracts to,
// and the two a healthy one does: a letter, and whitespace, which is a control
// character that is ordinary layout.
func TestIsUnmappedGlyph(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    rune
		want bool
	}{
		{"replacement character", unicode.ReplacementChar, true},
		{"private use", '', true},
		{"control", '\x01', true},
		{"tab", '\t', false},
		{"letter", 'a', false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnmappedGlyph(tc.r); got != tc.want {
				t.Errorf("isUnmappedGlyph(%q) = %t, want %t", tc.r, got, tc.want)
			}
		})
	}
}

// TestIsMeasurableLatinWord pins which words the vowel measure counts: four
// Latin letters or more, not all of them capitals.
func TestIsMeasurableLatinWord(t *testing.T) {
	for w, want := range map[string]bool{
		"word":  true,
		"Html":  true,
		"HTML":  false,
		"abc":   false,
		"ab1c":  false,
		"Ωmega": false,
	} {
		t.Run(w, func(t *testing.T) {
			if got := isMeasurableLatinWord(w); got != want {
				t.Errorf("isMeasurableLatinWord(%q) = %t, want %t", w, got, want)
			}
		})
	}
}
