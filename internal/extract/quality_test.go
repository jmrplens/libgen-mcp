package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	c, err := Extract(context.Background(), broken, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.QualityNote == "" {
		t.Errorf("QualityNote is empty for a damaged text layer: %+v", c)
	}

	healthy := filepath.Join(dir, "healthy.txt")
	if err := os.WriteFile(healthy, []byte(englishSample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Extract(context.Background(), healthy, Req{})
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
		if note := qualityNote(text); note != "" {
			t.Errorf("qualityNote(%q) = %q, want no note for a sample this short", text, note)
		}
	}
}
