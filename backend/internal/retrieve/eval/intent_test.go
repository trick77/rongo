package eval

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
)

// intentStages are the declared stage names the classifier is told about,
// the way the product hands it declaredStages().Names(). Without them the
// prompt says no stage is declared and "between" is always empty, so a
// release question could not be measured at all.
var intentStages = []string{"prod", "intg", "test"}

// intentCase is one question and the lane it must reach. want is the intent
// asserted exactly, for the two lanes the pipeline branches on; notLane says
// only that neither of them is taken, which is the real requirement for a
// question that must be searched: "how", "why" and "where" route the same
// way and the gate picking one over another changes nothing.
//
// The stages themselves are not asserted here: the classifier no longer
// reports them, releasePair reads them from the question's own words, and
// release_test.go covers that.
type intentCase struct {
	name     string
	question string
	follows  string
	want     string
	notLane  bool
	record   bool
}

// intentCases is the gate's routing measured where it is decided, which
// TestEvalMeasureAnswers cannot see: its rubrics grade answer prose and
// carry no intent, and its corpus declares no stages.
//
// The bug behind these: a turn asking how something is configured on prod,
// followed by "and how is it configured on intg", was classified release
// and answered from commits. recall puts both turns in one message, so the
// gate counted two stages across two questions and read a version delta
// out of a configuration question.
var intentCases = []intentCase{{
	name:     "config follow-up keeps the stage of its own question",
	question: "and how is it configured on intg?",
	follows:  "how is the request timeout configured on prod?",
	notLane:  true,
}, {
	name:     "a configuration difference across two stages is not a release",
	question: "what is the config difference on the request timeout between intg and prod?",
	notLane:  true,
}, {
	name:     "release notes are a release",
	question: "release notes between intg and prod",
	want:     ask.IntentRelease,
}, {
	name:     "release notes in German are a release",
	question: "Release Notes zwischen intg und prod",
	want:     ask.IntentRelease,
}, {
	name:     "a hyphen is not a different word",
	question: "release-notes between intg and prod",
	want:     ask.IntentRelease,
}, {
	// Recorded, not graded. The trigger moved off stage count, so this
	// phrasing asks for no notes and could have fallen to a search or to
	// "changes". Two runs said "release" both times, which is why nothing
	// was lost by narrowing the trigger; left recorded rather than asserted
	// because it is the gate's own reading, not a rule the product states.
	name:     "what is between two stages, with no notes asked for",
	question: "what is between prod and intg?",
	record:   true,
}}

// TestEvalUnderstandIntent measures the classifier's intent on the release
// gate, one short-gate call per case. Run it twice: a pinned gate call still
// re-rolls, and a one-case gap between runs says nothing.
//
//	BACKEND_EVAL=1 LLMWIRE_MIMO_API_KEY=... go test ./internal/retrieve/eval/ -run TestEvalUnderstandIntent -v
func TestEvalUnderstandIntent(t *testing.T) {
	requireEval(t)
	u := ask.NewUnderstander(evalLLM(t, 2*time.Minute))

	for _, tc := range intentCases {
		t.Run(tc.name, func(t *testing.T) {
			thread := ask.Thread{}
			if tc.follows != "" {
				thread.Question = tc.follows
			}
			var got ask.Understanding
			var last error
			for attempt := 1; attempt <= expandAttempts; attempt++ {
				got, last = u.Understand(context.Background(), tc.question, thread, intentStages)
				if last == nil {
					break
				}
				t.Logf("RETRY %d/%d: %v", attempt, expandAttempts, last)
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			if last != nil {
				t.Fatalf("understand: %v", last)
			}
			if tc.record {
				t.Logf("RECORDED intent=%q stage=%q", got.Intent, got.Stage)
				return
			}
			switch {
			case tc.notLane:
				if got.Intent == ask.IntentRelease || got.Intent == ask.IntentChanges {
					t.Errorf("intent = %q, want a searched turn (stage=%q)", got.Intent, got.Stage)
				}
			case got.Intent != tc.want:
				t.Errorf("intent = %q, want %q (stage=%q)", got.Intent, tc.want, got.Stage)
			}
		})
	}
}
