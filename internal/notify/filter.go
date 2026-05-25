package notify

import (
	"strings"

	homerun "github.com/stuttgart-things/homerun-library/v3"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
)

// Matches reports whether msg passes every filter rule in f.
//
// Combinator semantics (locked at Phase-1 review):
//   - severity_min: msg severity rank ≥ rule rank (unknown msg severity → info)
//   - match (map): every key/value pair must match the corresponding field
//     on msg, case-insensitive on both side
//   - tags_contain: at least one substring is present in msg.Tags (OR within
//     the list); the filter as a whole still AND-combines with the others
//   - message_contains: at least one substring is present in msg.Message
//
// All four rules AND together. Empty rules pass trivially, so an empty
// OutputFilters matches every message.
func Matches(f config.OutputFilters, msg homerun.Message) bool {
	if !severityAtLeast(msg.Severity, f.SeverityMin) {
		return false
	}
	for k, want := range f.Match {
		if !fieldEquals(msg, k, want) {
			return false
		}
	}
	if len(f.TagsContain) > 0 && !anySubstring(msg.Tags, f.TagsContain) {
		return false
	}
	if len(f.MessageContains) > 0 && !anySubstring(msg.Message, f.MessageContains) {
		return false
	}
	return true
}

// severityAtLeast returns true when msgSev's rank meets or exceeds minSev's.
// Empty minSev means "no floor". Unknown msg severities are treated as "info"
// so an upstream typo doesn't silently drop messages.
func severityAtLeast(msgSev, minSev string) bool {
	if strings.TrimSpace(minSev) == "" {
		return true
	}
	minRank, ok := config.SeverityRank(minSev)
	if !ok {
		// Validation should have caught this; be defensive and pass-through.
		return true
	}
	msgRank, ok := config.SeverityRank(msgSev)
	if !ok {
		msgRank, _ = config.SeverityRank("info")
	}
	return msgRank >= minRank
}

func fieldEquals(msg homerun.Message, key, want string) bool {
	got := getField(msg, key)
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func getField(msg homerun.Message, key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "severity":
		return msg.Severity
	case "system":
		return msg.System
	case "author":
		return msg.Author
	case "title":
		return msg.Title
	case "assigneename":
		return msg.AssigneeName
	case "assigneeaddress":
		return msg.AssigneeAddress
	default:
		return ""
	}
}

func anySubstring(haystack string, needles []string) bool {
	h := strings.ToLower(haystack)
	for _, n := range needles {
		if n == "" {
			continue
		}
		if strings.Contains(h, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
