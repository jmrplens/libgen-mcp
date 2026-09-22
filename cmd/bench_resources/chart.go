// chart.go draws the figures, as SVG, by hand.
//
// By hand because the alternative is a charting dependency in a module whose
// whole posture is that it has few of them, for three pictures. SVG is text, a
// line chart is a path and a bar chart is a rectangle, and the result is
// deterministic — which is what lets the gate compare the committed files byte
// for byte instead of asking a renderer whether it still agrees with itself.
//
// Everything here is drawn twice, once per scheme, because a figure with a dark
// ground on a light page is a hole in the page. The pair is what the site's
// picture element switches between.

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The figure's geometry, in user units. A viewBox rather than a fixed size, so
// the page decides how wide it is and the text scales with it.
const (
	chartWidth   = 720
	chartHeight  = 380
	marginLeft   = 64
	marginRight  = 24
	marginTop    = 48
	marginBottom = 64
)

// plotWidth and plotHeight are the panel the data is drawn in.
const (
	plotWidth  = chartWidth - marginLeft - marginRight
	plotHeight = chartHeight - marginTop - marginBottom
)

// gridChoices are the numbers of intervals the vertical axis may be divided
// into, which is one fewer than the labels it carries.
//
// More than one, because the axis is chosen by searching them: the label a
// reader actually reads is the step, and a step of a round size is only
// available at some counts. Four intervals of a max of 204 forces 62.5; five
// gives 50. Letting the count move is what keeps both the step round and the
// headroom small.
var gridChoices = []int{4, 5, 6}

// series is one line or one group of bars.
type series struct {
	Label  string
	Values []float64
}

// chart is a figure before it has been drawn.
type chart struct {
	Title    string
	Subtitle string
	// XLabels are the categories, one per value in every series.
	XLabels []string
	XTitle  string
	YTitle  string
	Series  []series
	// Bars draws grouped bars rather than lines, for a figure whose x axis is
	// a set of names rather than a quantity.
	Bars bool
}

// svg renders the figure in one scheme.
func (c chart) svg(p palette) string {
	maxY := c.maxValue()
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="%s">`,
		chartWidth, chartHeight, escapeXML(c.Title))
	b.WriteString("\n")
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/>`, chartWidth, chartHeight, p.Page)
	b.WriteString("\n")
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`,
		marginLeft, marginTop, plotWidth, plotHeight, p.Plot)
	b.WriteString("\n")

	c.writeTitles(&b, p)
	c.writeGrid(&b, p, maxY)
	c.writeXAxis(&b, p)
	if c.Bars {
		c.writeBars(&b, p, maxY)
	} else {
		c.writeLines(&b, p, maxY)
	}
	c.writeLegend(&b, p)
	b.WriteString("</svg>\n")
	return b.String()
}

// maxValue is the top of the vertical axis, rounded up to something a label can
// be written for. Zero never happens in practice and is handled anyway, because
// a chart that divided by it would not draw at all.
func (c chart) maxValue() float64 {
	highest := 0.0
	for _, s := range c.Series {
		for _, v := range s.Values {
			highest = math.Max(highest, v)
		}
	}
	if highest <= 0 {
		return 1
	}
	ceiling, _ := niceAxis(highest)
	return ceiling
}

// niceAxis picks the axis top and how many intervals reach it.
//
// Rounding the top and dividing it is the obvious thing and is what produced an
// axis of 0, 62.5, 125, 187.5, 250: the top was round and nothing between it and
// zero was. This rounds the step instead, over each allowed interval count, and
// keeps the arrangement whose top is closest to the data — so the labels read
// and the figure is not mostly empty.
func niceAxis(value float64) (ceiling float64, intervals int) {
	for _, count := range gridChoices {
		candidate := niceStep(value/float64(count)) * float64(count)
		if ceiling == 0 || candidate < ceiling {
			ceiling, intervals = candidate, count
		}
	}
	return ceiling, intervals
}

// niceStep rounds an interval up to one of a handful of shapes that read well:
// one, two, two and a half, four or five times a power of ten.
func niceStep(value float64) float64 {
	magnitude := math.Pow(10, math.Floor(math.Log10(value)))
	for _, step := range []float64{1, 2, 2.5, 4, 5, 10} {
		if candidate := step * magnitude; candidate >= value {
			return candidate
		}
	}
	return 10 * magnitude
}

// writeTitles puts the figure's name and its one-line explanation above the
// panel.
func (c chart) writeTitles(b *strings.Builder, p palette) {
	fmt.Fprintf(b, `<text x="%d" y="24" fill="%s" font-family="system-ui, sans-serif" font-size="16" font-weight="600">%s</text>`+"\n",
		marginLeft, p.Text, escapeXML(c.Title))
	if c.Subtitle != "" {
		fmt.Fprintf(b, `<text x="%d" y="40" fill="%s" font-family="system-ui, sans-serif" font-size="11">%s</text>`+"\n",
			marginLeft, p.Muted, escapeXML(c.Subtitle))
	}
}

// writeGrid draws the horizontal rules and the vertical axis labels.
func (c chart) writeGrid(b *strings.Builder, p palette, maxY float64) {
	_, intervals := niceAxis(maxY)
	for i := 0; i <= intervals; i++ {
		value := maxY * float64(intervals-i) / float64(intervals)
		y := marginTop + plotHeight*i/intervals
		fmt.Fprintf(b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="1"/>`+"\n",
			marginLeft, y, marginLeft+plotWidth, y, p.Grid)
		fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s" font-family="system-ui, sans-serif" font-size="11" text-anchor="end">%s</text>`+"\n",
			marginLeft-8, y+4, p.Muted, axisLabel(value))
	}
	if c.YTitle != "" {
		fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s" font-family="system-ui, sans-serif" font-size="11" transform="rotate(-90 %d %d)" text-anchor="middle">%s</text>`+"\n",
			14, marginTop+plotHeight/2, p.Muted, 14, marginTop+plotHeight/2, escapeXML(c.YTitle))
	}
}

// axisLabel writes a tick without a trailing run of zeros, so an axis reads
// 0, 50, 100 rather than 0.00, 50.00, 100.00.
func axisLabel(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// writeXAxis labels the categories under the panel.
func (c chart) writeXAxis(b *strings.Builder, p palette) {
	for i, label := range c.XLabels {
		x := c.xOf(i)
		fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s" font-family="system-ui, sans-serif" font-size="11" text-anchor="middle">%s</text>`+"\n",
			x, marginTop+plotHeight+18, p.Muted, escapeXML(label))
	}
	if c.XTitle != "" {
		fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s" font-family="system-ui, sans-serif" font-size="11" text-anchor="middle">%s</text>`+"\n",
			marginLeft+plotWidth/2, chartHeight-12, p.Muted, escapeXML(c.XTitle))
	}
}

// xOf is where the nth category sits, spread across the panel with a half step
// of padding at each end so the first and last points are not on the frame.
func (c chart) xOf(i int) int {
	n := len(c.XLabels)
	if n <= 1 {
		return marginLeft + plotWidth/2
	}
	return marginLeft + plotWidth*(2*i+1)/(2*n)
}

// yOf is where a value sits, with the panel's top at maxY.
func (c chart) yOf(value, maxY float64) float64 {
	return float64(marginTop) + float64(plotHeight)*(1-value/maxY)
}

// writeLines draws one polyline per series, with a dot at every measurement.
//
// The dots are not decoration: these axes are a handful of steps rather than a
// continuum, and a line without them invites a reader to take a point between
// two steps as something that was measured.
func (c chart) writeLines(b *strings.Builder, p palette, maxY float64) {
	for i, s := range c.Series {
		color := p.Series[i%len(p.Series)]
		points := make([]string, 0, len(s.Values))
		for j, value := range s.Values {
			points = append(points, fmt.Sprintf("%d,%s", c.xOf(j), coordinate(c.yOf(value, maxY))))
		}
		fmt.Fprintf(b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" points="%s"/>`+"\n",
			color, strings.Join(points, " "))
		for j, value := range s.Values {
			fmt.Fprintf(b, `<circle cx="%d" cy="%s" r="3" fill="%s"/>`+"\n",
				c.xOf(j), coordinate(c.yOf(value, maxY)), color)
		}
	}
}

// writeBars draws one group of bars per category.
func (c chart) writeBars(b *strings.Builder, p palette, maxY float64) {
	groups := len(c.XLabels)
	if groups == 0 {
		return
	}
	slot := plotWidth / groups
	barWidth := slot * 3 / (4 * max(len(c.Series), 1))
	for i, s := range c.Series {
		color := p.Series[i%len(p.Series)]
		for j, value := range s.Values {
			height := float64(plotHeight) * value / maxY
			x := c.xOf(j) - len(c.Series)*barWidth/2 + i*barWidth
			fmt.Fprintf(b, `<rect x="%d" y="%s" width="%d" height="%s" fill="%s"/>`+"\n",
				x, coordinate(float64(marginTop+plotHeight)-height), barWidth-2, coordinate(height), color)
		}
	}
}

// writeLegend names the series along the bottom of the figure.
func (c chart) writeLegend(b *strings.Builder, p palette) {
	x := marginLeft
	y := chartHeight - 30
	for i, s := range c.Series {
		color := p.Series[i%len(p.Series)]
		fmt.Fprintf(b, `<rect x="%d" y="%d" width="10" height="10" fill="%s"/>`+"\n", x, y-9, color)
		fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s" font-family="system-ui, sans-serif" font-size="11">%s</text>`+"\n",
			x+15, y, p.Text, escapeXML(s.Label))
		x += 20 + 7*len(s.Label)
	}
}

// coordinate writes a position with two decimals and no trailing zeros, so the
// committed files do not differ by a digit nobody can see.
func coordinate(value float64) string {
	return strconv.FormatFloat(round(value), 'f', -1, 64)
}

// escapeXML makes a label safe inside an SVG text node.
//
// Every label here is a scenario id or a number this command produced, so
// nothing hostile reaches it; it is escaped anyway because a figure is a
// published document and the day a label carries a catalog title is the day
// this would matter, which is exactly the day nobody would remember to add it.
func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}
