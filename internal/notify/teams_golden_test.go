package notify

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// go test ./internal/notify -run Golden -update rewrites testdata/cards.
var updateGolden = flag.Bool("update", false, "rewrite the golden card files")

// Golden messages, shared by the default and the rich layout.
var (
	goldenMergedPR = models.CaughtMessage{
		ObjectID: "abf0fc1e-7c1b-4b0e-9a57-3a3c1d0e2f11-homerun2-git-pitcher",
		Message: homerun.Message{
			Title:     "feat(platform-sthings): route the catcher's Teams outputs to their channels",
			Message:   "Merged by patrick-hermann-sva",
			Severity:  "success",
			Author:    "patrick-hermann-sva",
			System:    "homerun2-git-pitcher",
			Tags:      "github,pull_request,repo=stuttgart-things/stuttgart-things,number=4404,action=merged,head=feat/platform-sthings-teams-routing,base=main,actor=patrick-hermann-sva",
			Timestamp: "2026-11-07T21:47:06Z",
			URL:       "https://github.com/stuttgart-things/stuttgart-things/pull/4404",
		},
	}
	goldenAlert = models.CaughtMessage{
		ObjectID: "5d1c2b7e-alertmanager",
		Message: homerun.Message{
			Title:     "KubeJobFailed",
			Message:   "Job flux-system/image-reflector-cleanup failed to complete.",
			Severity:  "warning",
			Author:    "alertmanager",
			System:    "kubernetes",
			Tags:      "alertname=KubeJobFailed,namespace=flux-system,cluster=platform-sthings,status=firing",
			Timestamp: "2026-11-07T08:15:00+01:00",
			URL:       "https://grafana.example/alerting/list",
		},
	}
	goldenResolved = models.CaughtMessage{
		ObjectID: "5d1c2b7e-alertmanager-resolved",
		Message: homerun.Message{
			Title:     "KubeJobFailed",
			Message:   "Resolved: Job flux-system/image-reflector-cleanup failed to complete.",
			Severity:  "success",
			Author:    "alertmanager",
			System:    "kubernetes",
			Tags:      "alertname=KubeJobFailed,namespace=flux-system,cluster=platform-sthings,status=resolved",
			Timestamp: "2026-11-07T09:02:30Z",
		},
	}
	// A git-pitcher PR event with the title= tag (#84): title and message
	// repeat what header, subtitle and facts show.
	goldenMergedPRTitleTag = models.CaughtMessage{
		ObjectID: "4f2a9c3d-homerun2-git-pitcher",
		Message: homerun.Message{
			Title:     "PR #4408: chore(claim-machinery-api): v0.28.1 on labda machinery and platform-sthings (merged) on stuttgart-things/stuttgart-things",
			Message:   "PR merged by patrick-hermann-sva: chore/claim-machinery-api-v0.28.1 → main",
			Severity:  "success",
			Author:    "patrick-hermann-sva",
			System:    "homerun2-git-pitcher",
			Tags:      "github,pull_request,repo=stuttgart-things/stuttgart-things,number=4408,action=merged,head=chore/claim-machinery-api-v0.28.1,base=main,actor=patrick-hermann-sva,title=chore(claim-machinery-api): v0.28.1 on labda machinery and platform-sthings",
			Timestamp: "2026-11-08T10:12:44Z",
			URL:       "https://github.com/stuttgart-things/stuttgart-things/pull/4408",
		},
	}
	// A git-pitcher message from before git-pitcher#78: no url, no
	// key=value tags. The templates fall back.
	goldenBare = models.CaughtMessage{
		ObjectID: "c0ffee00-homerun2-git-pitcher",
		Message: homerun.Message{
			Title:     "push to main",
			Severity:  "info",
			Author:    "dependabot[bot]",
			System:    "homerun2-git-pitcher",
			Tags:      "github,push",
			Timestamp: "not a timestamp",
		},
	}
)

type goldenCase struct {
	name string
	msg  models.CaughtMessage
	card config.CardConfig
}

func checkGolden(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(BuildEnvelopeWithCard(tc.msg, tc.card), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "cards", tc.name+".json")
			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("card differs from %s (run with -update to accept):\n%s", path, got)
			}
		})
	}
}

// The default layout is locked byte for byte: rich (#82) is opt-in.
func TestGolden_DefaultLayout(t *testing.T) {
	facts := config.CardConfig{Facts: []config.CardFactConfig{
		{Title: "Repo", Tag: "repo"},
		{Title: "PR", Tag: "number"},
		{Title: "Zeit", Field: "timestamp"},
	}}
	checkGolden(t, []goldenCase{
		{"default-merged-pr", goldenMergedPR, config.CardConfig{}},
		{"default-alert", goldenAlert, config.CardConfig{}},
		{"default-resolved", goldenResolved, config.CardConfig{}},
		{"default-bare", goldenBare, config.CardConfig{}},
		{"default-merged-pr-facts", goldenMergedPR, facts},
	})
}

// The card configs of the docs' "Rich card layout" examples.
var (
	richGitCard = config.CardConfig{
		Layout:   "rich",
		Headline: config.Templates{"PR #{tag:number} {tag:action}"},
		Subtitle: config.Templates{"{tag:repo} · {tag:head} → {tag:base}", "{tag:repo}"},
		Avatar:   "github",
		Actions: []config.CardActionConfig{
			{Title: "PR öffnen", Field: "url"},
			{Title: "Diff", Template: "{field:url}/files"},
		},
	}
	richAlertCard = config.CardConfig{
		Layout:   "rich",
		Headline: config.Templates{"{tag:alertname}"},
		Subtitle: config.Templates{"{tag:namespace} · {tag:cluster}", "{field:system}"},
		Avatar:   "github",
		Actions:  []config.CardActionConfig{{Title: "Grafana", Field: "url"}},
	}
)

func TestGolden_RichLayout(t *testing.T) {
	withText := richGitCard
	withText.Text = config.Templates{"{tag:title}"}
	withFacts := richGitCard
	withFacts.Facts = []config.CardFactConfig{
		{Title: "Repo", Tag: "repo"},
		{Title: "Merged by", Tag: "actor"},
		{Title: "Zeit", Field: "timestamp"},
	}
	checkGolden(t, []goldenCase{
		{"rich-merged-pr", goldenMergedPR, richGitCard},
		{"rich-merged-pr-facts", goldenMergedPR, withFacts},
		{"rich-alert", goldenAlert, richAlertCard},
		{"rich-resolved", goldenResolved, richAlertCard},
		{"rich-bare", goldenBare, richGitCard},
		{"rich-layout-only", goldenAlert, config.CardConfig{Layout: "rich"}},
		{"rich-merged-pr-text", goldenMergedPRTitleTag, withText},
	})
}
