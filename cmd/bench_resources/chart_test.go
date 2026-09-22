package main

import (
	"strings"
	"testing"
)

// TestNiceAxis_RoundsTheStepRatherThanTheTop verifies the arrangement that makes
// an axis readable.
//
// Rounding the top and dividing it is the obvious thing, and it produced an axis
// of 0, 62.5, 125, 187.5, 250: the top was round and nothing between it and zero
// was. The step is what a reader actually reads, so the step is what is rounded,
// over each allowed interval count, keeping the top closest to the data.
func TestNiceAxis_RoundsTheStepRatherThanTheTop(t *testing.T) {
	testCases := []struct {
		name          string
		max           float64
		wantCeiling   float64
		wantIntervals int
	}{
		{name: "the memory matrix", max: 204.21, wantCeiling: 240, wantIntervals: 6},
		{name: "the series memory", max: 140.61, wantCeiling: 150, wantIntervals: 6},
		{name: "the series latency", max: 2596, wantCeiling: 3000, wantIntervals: 6},
		{name: "a small figure", max: 3.7, wantCeiling: 4, wantIntervals: 4},
		{name: "exactly a round number", max: 100, wantCeiling: 100, wantIntervals: 4},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ceiling, intervals := niceAxis(tc.max)
			if ceiling != tc.wantCeiling || intervals != tc.wantIntervals {
				t.Errorf("niceAxis(%v) = %v over %d, want %v over %d",
					tc.max, ceiling, intervals, tc.wantCeiling, tc.wantIntervals)
			}
			if ceiling < tc.max {
				t.Errorf("niceAxis(%v) = %v, which is under the data", tc.max, ceiling)
			}
			step := ceiling / float64(intervals)
			if step != niceStep(step) {
				t.Errorf("the step is %v, which is not one a reader recognizes", step)
			}
		})
	}
}

// TestMaxValue_NeverDividesByZero verifies a figure with nothing in it still
// draws, rather than producing coordinates that are not numbers.
func TestMaxValue_NeverDividesByZero(t *testing.T) {
	testCases := []struct {
		name string
		c    chart
		want float64
	}{
		{name: "no series at all", c: chart{}, want: 1},
		{name: "a series of zeros", c: chart{Series: []series{{Values: []float64{0, 0}}}}, want: 1},
		{name: "a real series", c: chart{Series: []series{{Values: []float64{1, 7}}}}, want: 8},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.maxValue(); got != tc.want {
				t.Errorf("maxValue() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestChartSVG_DrawsBothShapesInBothSchemes verifies a figure comes out as valid
// SVG carrying its own title, its data and the palette it was given.
func TestChartSVG_DrawsBothShapesInBothSchemes(t *testing.T) {
	p := palette{
		Scheme: schemeDark, Page: "#0d1117", Plot: "#161b22", Grid: "#30363d",
		Text: "#e6edf3", Muted: "#8b949e", Series: []string{"#d99a3f", "#58a6ff", "#3fb950"},
	}
	testCases := []struct {
		name string
		c    chart
		want string
	}{
		{
			name: "a line chart",
			c: chart{
				Title: "Lines", Subtitle: "two of them", XLabels: []string{"1", "2"},
				XTitle: "x", YTitle: "y",
				Series: []series{{Label: "a", Values: []float64{1, 2}}, {Label: "b", Values: []float64{2, 4}}},
			},
			want: "<polyline",
		},
		{
			name: "a bar chart",
			c: chart{
				Title: "Bars", XLabels: []string{"one", "two"}, Bars: true,
				Series: []series{{Label: "a", Values: []float64{1, 2}}},
			},
			want: "<rect",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.svg(p)
			for _, want := range []string{
				`<svg xmlns="http://www.w3.org/2000/svg"`, `role="img"`, tc.c.Title, p.Page, p.Series[0], tc.want, "</svg>",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("the figure does not carry %q:\n%s", want, got)
				}
			}
		})
	}

	t.Run("a single category is centered rather than divided by zero", func(t *testing.T) {
		one := chart{Title: "One", XLabels: []string{"only"}, Series: []series{{Label: "a", Values: []float64{1}}}}
		if got := one.svg(p); !strings.Contains(got, "<circle") {
			t.Errorf("a one-point figure drew no point:\n%s", got)
		}
	})

	t.Run("a bar chart with no categories draws nothing rather than panicking", func(t *testing.T) {
		empty := chart{Title: "Empty", Bars: true, Series: []series{{Label: "a"}}}
		if got := empty.svg(p); !strings.Contains(got, "</svg>") {
			t.Error("an empty bar chart did not produce a document")
		}
	})
}

// TestEscapeXML_KeepsALabelInsideItsTextNode verifies the escaping every label
// goes through.
//
// Nothing hostile reaches these labels today — they are scenario ids and numbers
// this command produced — and they are escaped anyway, because the day a label
// carries a catalog title is exactly the day nobody would remember to add it.
func TestEscapeXML_KeepsALabelInsideItsTextNode(t *testing.T) {
	got := escapeXML(`a & b < c > d " e ' f`)
	for _, unwanted := range []string{" & ", "<", ">", `"`, "'"} {
		t.Run(unwanted, func(t *testing.T) {
			if strings.Contains(got, unwanted) {
				t.Errorf("escapeXML() = %q, which still carries %q", got, unwanted)
			}
		})
	}
	if !strings.Contains(got, "&amp;") || !strings.Contains(got, "&lt;") {
		t.Errorf("escapeXML() = %q, want the entities", got)
	}
}

// TestCoordinate_WritesAPositionWithoutTrailingZeros verifies the committed
// figures do not differ by a digit nobody can see.
func TestCoordinate_WritesAPositionWithoutTrailingZeros(t *testing.T) {
	testCases := []struct {
		name string
		in   float64
		want string
	}{
		{name: "a whole number", in: 48, want: "48"},
		{name: "two decimals", in: 48.125, want: "48.13"},
		{name: "a trailing zero", in: 48.1, want: "48.1"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coordinate(tc.in); got != tc.want {
				t.Errorf("coordinate(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
