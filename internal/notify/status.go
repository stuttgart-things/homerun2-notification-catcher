package notify

import (
	"strings"

	homerun "github.com/stuttgart-things/homerun-library/v4"
)

// Alert states, as omni-pitcher's /pitch/alertmanager tags them (#71).
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// resolvedPrefix opens the message of a resolved alert on omni-pitcher's
// /pitch/grafana, which sets no status tag.
const resolvedPrefix = "Resolved: "

// AlertStatus is the alert state of msg: the value of its `status=` tag when
// that is firing or resolved, else resolved when the message opens with
// "Resolved: " (/pitch/grafana), else "" -- not an alert, or one that does
// not say.
func AlertStatus(msg homerun.Message) string {
	switch s := strings.ToLower(tagValue(msg.Tags, "status")); s {
	case StatusFiring, StatusResolved:
		return s
	}
	if hasPrefixFold(strings.TrimSpace(msg.Message), resolvedPrefix) {
		return StatusResolved
	}
	return ""
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
