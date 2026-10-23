package server

import "testing"

func TestTranslatedLogSeverity(t *testing.T) {
	for _, tc := range []struct{ message, level string }{
		{"[mcp] browser connection failed: dial tcp: connect: connection refused", "error"},
		{"[queue] dropped pending work", "error"},
		{"[runner] target unreachable", "error"},
		{"[provider] retrying after transient failure", "warn"},
		{"[mcp] 连接失败", "error"},
		{"[provider] 重试", "warn"},
		{"[runner] connected", "info"},
	} {
		if got := parseLog(tc.message).Level; got != tc.level {
			t.Errorf("%q level=%s want=%s", tc.message, got, tc.level)
		}
	}
}
