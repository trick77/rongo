package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/modules"
)

// TestLocateAnswer runs the flagship question through the REAL pipeline, once
// with the locate loop off and once with it on, and prints both answers.
//
// Every other arm in this package stops at what reached the sources. That is
// not the question: the converter has been at hop 0 of the hit list all along,
// its chunk carries the mapping line, and the answer was still wrong. What the
// loop is for is the step after gathering — so the only measurement that says
// whether it works is the text a reader would see.
//
// No rubric and no judge. A judged score over twenty questions is the arm that
// decides whether this ships; this one is for reading the two answers side by
// side and seeing whether the second names the converter.
func TestLocateAnswer(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	question, needle, file := flagshipCase(t)
	// The line a correct answer names. Reported, never asserted: the point is
	// to read the answers, and a substring check would turn a German sentence
	// about the right place into a pass or a fail on spelling.
	// The place the question turns on: the converter class, and the setter
	// in it. Asked as Developer, because an Analyst answer is written
	// without identifiers on purpose and could name neither.
	wanted := []string{strings.TrimSuffix(file, filepath.Ext(file)), needle}

	c := answerLLM(t)
	mo := modules.Opts{MinChunks: envIntOr(t, "BACKEND_MODULE_MIN_CHUNKS", 8),
		MaxChunks: envIntOr(t, "BACKEND_MODULE_MAX_CHUNKS", 150)}
	opts := gatherOpts(t)

	for _, arm := range []struct {
		name   string
		rounds int
	}{{"loop off", 0}, {"loop on", 1}, {"loop on, 3 rounds", 3}} {
		retriever := evalRetriever(t, db)
		retriever.Reranker = evalReranker(t, c)
		g := ask.NewGatherer(db, opts)
		if arm.rounds > 0 {
			g = g.WithLocateLoop(c, retriever).WithLocateRounds(arm.rounds)
		}
		// No process models: this question is a field mapping, not a BPMN
		// flow, and the listing would only add prompt the answer does not use.
		pipeline := ask.NewPipeline(c, retriever, g, ask.NewRouter(c, db, routeMargin(t), mo))

		started := time.Now()
		answer, clar, err := pipeline.Run(ctx, question, ask.AudienceDev, ask.LanguageDE, ask.Thread{}, ask.Events{})
		switch {
		case err != nil:
			t.Errorf("\n=== %s: FAILED: %v", arm.name, err)
			continue
		case clar != nil:
			t.Errorf("\n=== %s: asked back instead of answering", arm.name)
			continue
		}

		var names []string
		for _, w := range wanted {
			names = append(names, fmt.Sprintf("%s=%v", w, strings.Contains(answer.Text, w)))
		}
		t.Logf("\n=== %s — %d sources, %s, %d tokens, names %s\n"+
			"    pointer: %q\n"+
			"    converter: source #%d of %d, first cited as [%d], first named in paragraph %d\n%s",
			arm.name, len(answer.Sources), time.Since(started).Round(time.Second).String(),
			answer.Usage.Total, strings.Join(names, " "),
			answer.Scope.Located,
			sourceRank(answer.Sources, file), len(answer.Sources),
			firstMarker(answer.Citations, file), firstParagraph(answer.Text, wanted[0]),
			answer.Text)
		for _, cit := range answer.Citations {
			t.Logf("    cited %s %s:%d-%d", cit.Repo, cit.Path, cit.StartLine, cit.EndLine)
		}
	}
}

// flagshipCase is the flagship question, the identifier a correct answer
// names and the file holding it. Private code, so read from the environment
// the corpus runner sets and never written into this repository; a run
// without them is skipped, not guessed.
func flagshipCase(t *testing.T) (question, needle, file string) {
	t.Helper()
	question = os.Getenv("BACKEND_EVAL_LOCATE_QUESTION")
	needle = os.Getenv("BACKEND_EVAL_LOCATE_NEEDLE")
	file = os.Getenv("BACKEND_EVAL_LOCATE_FILE")
	if question == "" || needle == "" || file == "" {
		t.Skip("BACKEND_EVAL_LOCATE_QUESTION, _NEEDLE and _FILE are unset; the flagship runs from the private corpus")
	}
	return question, needle, file
}

// sourceRank is the 1-based position of the first source in file among the
// numbered sources the answer was written from, or 0 when none is.
func sourceRank(sources []ask.Source, file string) int {
	for i, s := range sources {
		if strings.HasSuffix(s.Path, file) {
			return i + 1
		}
	}
	return 0
}

// firstMarker is the lowest citation marker pointing into file, or 0.
func firstMarker(cits []ask.Citation, file string) int {
	best := 0
	for _, c := range cits {
		if strings.HasSuffix(c.Path, file) && (best == 0 || c.Marker < best) {
			best = c.Marker
		}
	}
	return best
}

// firstParagraph is the 1-based paragraph of text that first names word, or
// 0 when the answer never does. Paragraph 1 is the answer's opening
// sentence: a place named there is the place the answer leads with.
func firstParagraph(text, word string) int {
	for i, p := range strings.Split(text, "\n\n") {
		if strings.Contains(p, word) {
			return i + 1
		}
	}
	return 0
}
