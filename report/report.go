// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	Facts       []*db.Node
	Intents     []*db.Node
	Artifacts   []Artifact
	Assessments []Assessment
	Limitations []string
}

// Artifact paths are canonical workspace-relative paths, never model-supplied URLs.
type Artifact struct {
	Path string
	Size int64
	URL  string
}

type Assessment struct {
	Path    string
	Content string
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Penetration test report — %s\n\n", nz(in.Title, "Unnamed task"))
	fmt.Fprintf(&b, "- **Task objectives**: %s\n", nz(in.Goal, "(unspecified)"))
	fmt.Fprintf(&b, "- **Generated at**: %s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- Confirmed findings: **%d**\n", len(in.Findings))
	fmt.Fprintf(&b, "- Asset:")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## Findings\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_No vulnerabilities confirmed in this run._\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n))
		}
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "Uncategorized")))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / Evidence:**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	b.WriteString("## Assessment coverage and limits\n\n")
	b.WriteString("A completed task or an empty findings list does not prove the target is secure. The observations and artifacts below describe the work actually recorded. Unexercised capabilities are not a pass.\n\n")
	for _, limit := range in.Limitations {
		fmt.Fprintf(&b, "- %s\n", limit)
	}
	for _, n := range in.Intents {
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		fmt.Fprintf(&b, "- Intent #%d — %s: %s\n", n.ID, n.State, p.Summary)
		if n.BlockedReason != "" {
			fmt.Fprintf(&b, "  Blocked: %s\n", n.BlockedReason)
		}
	}
	b.WriteString("\n## Worker assessments\n\n")
	if len(in.Assessments) == 0 {
		b.WriteString("No report.md or assessment.md artifact was found. Review the recorded observations below.\n\n")
	}
	for _, assessment := range in.Assessments {
		fmt.Fprintf(&b, "### %s\n\n", assessment.Path)
		b.WriteString(assessment.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("## Recorded observations and evidence\n\n")
	for _, n := range in.Facts {
		fmt.Fprintf(&b, "### Observation #%d\n\n", n.ID)
		// Preserve the complete structured evidence, including negative results.
		// A fence longer than any payload backtick run prevents accidental breakout.
		fence := "```"
		for strings.Contains(string(n.Payload), fence) {
			fence += "`"
		}
		fmt.Fprintf(&b, "%sjson\n%s\n%s\n\n", fence, n.Payload, fence)
	}
	b.WriteString("## Artifact index\n\nPaths are relative to the ARTEX workspace. Downloads require an authenticated ARTEX session.\n\n")
	for _, artifact := range in.Artifacts {
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "\n", " ", "\r", " ").Replace(artifact.Path)
		fmt.Fprintf(&b, "- [%s](%s) — %d bytes\n", label, artifact.URL, artifact.Size)
	}
	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
