package config

import (
	"reflect"
	"testing"
)

func TestParseStreams(t *testing.T) {
	cases := []struct {
		name       string
		streamsEnv string
		streamFall string
		want       []string
	}{
		{"multi from REDIS_STREAMS", "alerts,releases", "ignored", []string{"alerts", "releases"}},
		{"whitespace trimmed", " alerts , releases ", "", []string{"alerts", "releases"}},
		{"empty entries dropped", "alerts,,releases,", "", []string{"alerts", "releases"}},
		{"legacy REDIS_STREAM single", "", "alerts", []string{"alerts"}},
		{"empty streams env falls back to legacy", "", "messages", []string{"messages"}},
		{"both unset returns hardcoded default", "", "", []string{"alerts"}},
		{"streams with only whitespace falls back to legacy", " , , ", "legacy", []string{"legacy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseStreams(tc.streamsEnv, tc.streamFall)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseStreams(%q, %q) = %v, want %v", tc.streamsEnv, tc.streamFall, got, tc.want)
			}
		})
	}
}
