package server

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

// fragmentEvents is how many dispatched messages the portal fragment shows.
const fragmentEvents = 8

// fragmentRefreshSeconds is how often the portal reloads the fragment.
const fragmentRefreshSeconds = 10

// Manifest is the homerun2-portal fragment contract (#77): the portal reads
// <BASE_PATH>/ui/manifest.json and loads each fragment into the service's
// card on its overview.
type Manifest struct {
	Title     string     `json:"title"`
	Version   string     `json:"version,omitempty"`
	Fragments []Fragment `json:"fragments"`
}

// Fragment is one HTML partial of the manifest; URL carries BASE_PATH, the
// fragment runs on the portal's origin.
type Fragment struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	RefreshSeconds int    `json:"refreshSeconds"`
}

// The fragment is bare HTML on the portal's hr-* classes: no <html>, no
// scripts, every message field escaped by html/template.
var fragmentTmpl = template.Must(template.New("fragment").Funcs(template.FuncMap{
	"sev":     sevClass,
	"outcome": outcome,
}).Parse(`<div class="hr-frag">
{{- if .Events}}
<ul class="hr-list">
{{- range .Events}}
<li class="hr-item"><span class="hr-time">{{.At.Format "15:04:05"}}</span><span class="hr-sev hr-sev-{{sev .Severity}}">{{if .Severity}}{{.Severity}}{{else}}info{{end}}</span>{{with .System}}<span class="hr-system">{{.}}</span>{{end}}<span class="hr-title">{{.Title}}</span><span class="hr-muted">{{outcome .}}</span></li>
{{- end}}
</ul>
{{- else}}
<p class="hr-muted">No messages dispatched yet.</p>
{{- end}}
<a class="hr-frag-more" href="{{.BasePath}}/events">All events &rarr;</a>
</div>
`))

// sevClass maps a message severity onto the portal's hr-sev-* classes.
func sevClass(severity string) string {
	switch s := strings.ToLower(severity); s {
	case "critical", "error":
		return "error"
	case "warning", "success", "debug":
		return s
	default:
		return "info"
	}
}

// outcome says in a few words where a message went.
func outcome(ev Event) string {
	if !ev.Routed {
		return "no output matched"
	}
	var sent, failed, dry []string
	for _, o := range ev.Outputs {
		switch o.Result {
		case notify.ResultSent:
			sent = append(sent, o.Output)
		case notify.ResultFailed:
			failed = append(failed, o.Output)
		case notify.ResultDryRun:
			dry = append(dry, o.Output)
		}
	}
	var parts []string
	if len(sent) > 0 {
		parts = append(parts, "→ "+strings.Join(sent, ", "))
	}
	if len(dry) > 0 {
		parts = append(parts, "dry-run "+strings.Join(dry, ", "))
	}
	if len(failed) > 0 {
		parts = append(parts, "failed "+strings.Join(failed, ", "))
	}
	return strings.Join(parts, " · ")
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Manifest{
		Title:   "Notification Catcher",
		Version: s.opts.Build.Version,
		Fragments: []Fragment{{
			ID:             "events",
			Title:          "Latest notifications",
			URL:            s.opts.BasePath + "/ui/fragment/events",
			RefreshSeconds: fragmentRefreshSeconds,
		}},
	})
}

func (s *Server) handleEventsFragment(w http.ResponseWriter, _ *http.Request) {
	var buf bytes.Buffer
	err := fragmentTmpl.Execute(&buf, struct {
		BasePath string
		Events   []Event
	}{s.opts.BasePath, s.events.list(EventQuery{Limit: fragmentEvents})})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
