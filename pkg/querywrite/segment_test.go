package querywrite

import (
	"strings"
	"testing"
)

func TestSegmentReason(t *testing.T) {
	refused := map[string]string{
		"":                       "empty",
		".":                      `"."`,
		"..":                     `".."`,
		"~":                      "queries root",
		"a\x00b":                 "NUL",
		"a\xffb":                 "UTF-8",
		"a\x07b":                 "control",
		"a\tb":                   "control",
		"rev\u202eenue":          "bidirectional",
		"a\u200fb":               "bidirectional",
		"rev\u200cenue":          "invisible",
		"rev\u200denue":          "invisible",
		"rev\u200benue":          "invisible",
		"rev\u2060enue":          "invisible",
		"rev\u206fenue":          "invisible",
		"rev\ufeffenue":          "invisible",
		"rev\u00adenue":          "invisible",
		"rev\u034fenue":          "invisible",
		"chart\ufe0f":            "invisible",
		"tag\U000e0001":          "invisible",
		"a\u0378b":               "unassigned",
		"a\ufdd0b":               "unassigned",
		"a\ue000b":               "unassigned",
		"a\u2028b":               "unassigned",
		"LONGFO~1":               "short name",
		"report~12.txt":          "short name",
		"a~9":                    "short name",
		"a/b":                    "any of",
		`a\b`:                    "any of",
		"c:q":                    "any of",
		"a<b":                    "any of",
		"a?b":                    "any of",
		".dt-query-txn":          "reserved",
		".hidden":                `start with "."`,
		"q.":                     `end with "."`,
		"q ":                     `end with "."`,
		strings.Repeat("q", 201): "200 bytes",
		"CON":                    "device",
		"con.txt":                "device",
		"LPT1":                   "device",
		"com\u00b9":              "device",
		"NUL .query":             "device",
		"CONOUT$":                "device",
	}
	for segment, want := range refused {
		reason, ok := SegmentReason(segment)
		if ok || !strings.Contains(reason, want) {
			t.Errorf("SegmentReason(%q) = (%q, %v), want a refusal mentioning %q", segment, reason, ok, want)
		}
	}
	for _, segment := range []string{
		"revenue", "customer-invoices", "q1", "a.b", "café", "~x", "x~", "CONSOLE", strings.Repeat("q", 200),
		"rev~enue", "report~", "日本語", "naïve", "revenue 2026", "q~1a", "longfolder~1x",
	} {
		if reason, ok := SegmentReason(segment); !ok {
			t.Errorf("SegmentReason(%q) refused it: %s", segment, reason)
		}
	}
}
