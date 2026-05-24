package models

// TeamsEnvelope is the outer Power Automate "post-message-in-chat" payload.
// MS Teams' Adaptive-Card webhooks (and the Power Automate flow that replaces
// the legacy O365 connector) expect a top-level message with one attachment
// containing the actual Adaptive Card content.
type TeamsEnvelope struct {
	Type        string            `json:"type"`
	Attachments []TeamsAttachment `json:"attachments"`
}

// TeamsAttachment wraps an Adaptive Card inside the envelope.
type TeamsAttachment struct {
	ContentType string       `json:"contentType"`
	ContentURL  *string      `json:"contentUrl"`
	Content     AdaptiveCard `json:"content"`
}

// AdaptiveCard is a minimal subset of the Adaptive Card v1.4 schema — enough
// to render a homerun.Message as a styled card with optional URL action.
type AdaptiveCard struct {
	Schema  string        `json:"$schema"`
	Type    string        `json:"type"`
	Version string        `json:"version"`
	Body    []CardElement `json:"body"`
	Actions []CardAction  `json:"actions,omitempty"`
}

// CardElement is one node in the card body. We model only the subset we emit
// (Container, TextBlock, FactSet); each carries the union of fields used by
// those types and serialises with `omitempty` so unused fields disappear from
// the wire payload.
type CardElement struct {
	Type   string        `json:"type"`
	Style  string        `json:"style,omitempty"`
	Items  []CardElement `json:"items,omitempty"`
	Text   string        `json:"text,omitempty"`
	Size   string        `json:"size,omitempty"`
	Weight string        `json:"weight,omitempty"`
	Wrap   bool          `json:"wrap,omitempty"`
	Color  string        `json:"color,omitempty"`
	Facts  []CardFact    `json:"facts,omitempty"`
}

// CardFact is one row in a FactSet.
type CardFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// CardAction is one entry in the card's action bar. We only emit Action.OpenUrl.
type CardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}
