package models

// TeamsEnvelope is the outer Power Automate "post-message-in-chat" payload.
// MS Teams' Adaptive-Card webhooks (and the Power Automate flow that replaces
// the legacy O365 connector) expect a top-level message with one attachment
// containing the actual Adaptive Card content.
type TeamsEnvelope struct {
	Type string `json:"type"`
	// Summary is the message's preview text (notification, activity feed,
	// chat list), the card's fallbackText again: a host that previews a card
	// reads the message's summary, not the card's own fields (#45).
	Summary     string            `json:"summary,omitempty"`
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
	Schema  string `json:"$schema"`
	Type    string `json:"type"`
	Version string `json:"version"`
	// FallbackText is the plain text a host shows when it cannot render the
	// card: notification previews and some Teams clients, which otherwise
	// show only "Card - access it on https://go.skype.com/cards.unsupported".
	FallbackText string `json:"fallbackText,omitempty"`
	// MSTeams carries the Teams-only card properties (rich layout, #82).
	MSTeams *CardMSTeams  `json:"msteams,omitempty"`
	Body    []CardElement `json:"body"`
	Actions []CardAction  `json:"actions,omitempty"`
}

// CardMSTeams is the card's "msteams" property; Width "Full" spans the
// channel's width.
type CardMSTeams struct {
	Width string `json:"width,omitempty"`
}

// CardElement is one node in the card body. We model only the subset we emit
// (Container, TextBlock, FactSet; ColumnSet, Column and Image for the rich
// layout); each carries the union of fields used by
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
	// IsSubtle dims a TextBlock (the objectId footer).
	IsSubtle bool       `json:"isSubtle,omitempty"`
	Facts    []CardFact `json:"facts,omitempty"`

	// Used by the rich layout (#82) only.
	ID        string `json:"id,omitempty"`
	IsVisible *bool  `json:"isVisible,omitempty"`
	Bleed     bool   `json:"bleed,omitempty"`
	Spacing   string `json:"spacing,omitempty"`
	FontType  string `json:"fontType,omitempty"`
	// Columns are a ColumnSet's Column elements.
	Columns []CardElement `json:"columns,omitempty"`
	// Width and VerticalContentAlignment lay out a Column.
	Width                    string `json:"width,omitempty"`
	VerticalContentAlignment string `json:"verticalContentAlignment,omitempty"`
	// URL and AltText are an Image's; its Style "Person" crops it round.
	URL     string `json:"url,omitempty"`
	AltText string `json:"altText,omitempty"`
}

// CardFact is one row in a FactSet.
type CardFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// CardAction is one entry in the card's action bar: Action.OpenUrl, and
// Action.ToggleVisibility for the rich layout's Details button.
type CardAction struct {
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	URL            string   `json:"url,omitempty"`
	TargetElements []string `json:"targetElements,omitempty"`
}
