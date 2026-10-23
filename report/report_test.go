package report

import (
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"strings"
	"testing"
)

func TestMarkdownIncludesNegativeEvidenceAndAssessment(t *testing.T) {
	got := Markdown(Input{Title: "baseline", Facts: []*db.Node{{ID: 3, Payload: json.RawMessage(`{"summary":"ten timeouts","evidence":"43 bytes"}`)}}, Intents: []*db.Node{{ID: 2, State: "failed", BlockedReason: "browser unavailable"}}, Assessments: []Assessment{{Path: "tasks/1/i2/report.md", Content: "No HTTP application reached."}}, Artifacts: []Artifact{{Path: "tasks/1/i2/probes.jsonl", Size: 43, URL: "/api/workspace/download?path=tasks%2F1%2Fi2%2Fprobes.jsonl"}}, Limitations: []string{"Browser actions untested."}})
	for _, want := range []string{"ten timeouts", "43 bytes", "No HTTP application reached.", "browser unavailable", "Browser actions untested.", "does not prove the target is secure", "tasks%2F1%2Fi2%2Fprobes.jsonl"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}
