package instructions

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// stepTool matches a numbered workflow line and captures the tool it names.
var stepTool = regexp.MustCompile(`(?m)^\d+\. (\w+):`)

// numberedTools returns the tools the workflow numbers, in order.
func numberedTools(text string) []string {
	var tools []string
	for _, m := range stepTool.FindAllStringSubmatch(text, -1) {
		tools = append(tools, m[1])
	}
	return tools
}

// TestRenderNumbersTheStepsEachVariantServes pins which tools each deployment
// walks a model through, and in what order. A deployment that does not fetch
// has no read tool, so it numbers three steps and says why.
func TestRenderNumbersTheStepsEachVariantServes(t *testing.T) {
	tests := []struct {
		name        string
		serverFetch bool
		linkOnly    bool
		want        []string
		noFetchNote bool
	}{
		{"local", true, false, []string{"search", "get_details", "download", "read"}, false},
		{"remote with read", true, true, []string{"search", "get_details", "download", "read"}, false},
		{"remote without read", false, true, []string{"search", "get_details", "download"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := Render(tt.serverFetch, tt.linkOnly)
			if got := numberedTools(text); !slices.Equal(got, tt.want) {
				t.Errorf("numbered tools = %v, want %v", got, tt.want)
			}
			if got := strings.Contains(text, noFetch); got != tt.noFetchNote {
				t.Errorf("no-fetch note present = %v, want %v", got, tt.noFetchNote)
			}
			if got := strings.HasPrefix(text, opening); got != tt.serverFetch {
				t.Errorf("opens with the reading opening = %v, want %v", got, tt.serverFetch)
			}
			if !strings.HasSuffix(text, prompts) {
				t.Error("the prompts paragraph is not last")
			}
		})
	}
}

// TestRenderStatesTheDownloadContract checks the download step says what the
// deployment does: save the file, or return a link and never a saved file.
func TestRenderStatesTheDownloadContract(t *testing.T) {
	if !strings.Contains(Render(true, false), stepDownload) {
		t.Error("a saving deployment does not say download saves the file")
	}
	for _, v := range Variants()[1:] {
		t.Run(v.Name, func(t *testing.T) {
			text := Render(v.ServerFetch, v.LinkOnly)
			if !strings.Contains(text, stepDownloadLinkOnly) || strings.Contains(text, "save the file") {
				t.Errorf("a link-only deployment is told something else:\n%s", text)
			}
		})
	}
}

// TestVariantsCoverEveryDeployment holds the list an audit walks to the three
// deployments that exist, so a served variant cannot be skipped.
func TestVariantsCoverEveryDeployment(t *testing.T) {
	want := []Variant{
		{Name: "local", ServerFetch: true, LinkOnly: false},
		{Name: "remote with read", ServerFetch: true, LinkOnly: true},
		{Name: "remote without read", ServerFetch: false, LinkOnly: true},
	}
	if got := Variants(); !slices.Equal(got, want) {
		t.Errorf("Variants() = %v, want %v", got, want)
	}
}

// TestRenderIsGatewaySafe holds every variant to the served-text rule, ASCII
// prose with no semicolon, here as well as in cmd/audit_gateway_chars, so the
// package that owns the text fails first.
func TestRenderIsGatewaySafe(t *testing.T) {
	for _, v := range Variants() {
		t.Run(v.Name, func(t *testing.T) {
			text := Render(v.ServerFetch, v.LinkOnly)
			for i, r := range text {
				if r > 0x7f || r == ';' {
					t.Errorf("offset %d carries %q", i, r)
				}
			}
		})
	}
}

// TestDetailsStepNamesTheCitationArguments keeps the get_details step naming
// the arguments a citation request needs, which the tool list alone does not
// connect to the workflow.
func TestDetailsStepNamesTheCitationArguments(t *testing.T) {
	for _, arg := range []string{"citation", "cite_as", "related", "BibTeX", "RIS"} {
		t.Run(arg, func(t *testing.T) {
			if !strings.Contains(stepDetails, arg) {
				t.Errorf("get_details step does not mention %s", arg)
			}
		})
	}
}
