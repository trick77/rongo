package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// fakeSearch records the query it was handed and returns fixed hits.
type fakeSearch struct {
	hits []retrieve.Hit
	got  retrieve.Query
}

func (f *fakeSearch) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	f.got = q
	return f.hits, nil
}

// twoStepUpstream answers the understanding call with JSON and the answer call
// with a stream, telling them apart by whether streaming was requested.
func twoStepUpstream(t *testing.T, understanding string, answerTokens ...string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": understanding}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := http.NewResponseController(w)
		for _, tok := range answerTokens {
			frame, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			_ = fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
}

func TestPipeline_searchesWithTheExpansionNotJustTheQuestion(t *testing.T) {
	// The measured point of this phase. Searching the raw question alone is
	// what left the six questions at ranks 23 to 52.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "backend/internal/playbackgrant/store.go", 0, 1, 20, "NewGrant", "func NewGrant() {}")
	search := &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}}
	c := twoStepUpstream(t, appleTVReply, "Der Zugriff laeuft ueber einen Grant [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}))
	q := "Wie kommt ein Apple TV ohne Anmeldung an die Mediendatei?"

	got, err := p.Run(context.Background(), q, AudienceBA, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(search.got.Texts) < 2 {
		t.Fatalf("search texts = %v, want the question plus expansions", search.got.Texts)
	}
	if !strings.Contains(strings.ToLower(strings.Join(search.got.Texts, " ")), "airplay") {
		t.Errorf("search texts = %v, want the guessed code vocabulary", search.got.Texts)
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "peeq" {
		t.Errorf("repos = %v, want the hint from the understanding step", search.got.Repos)
	}
	if len(got.Citations) != 1 {
		t.Errorf("citations = %+v", got.Citations)
	}
}

func TestPipeline_nothingFoundNamesTheTermsItTried(t *testing.T) {
	// A dead end someone can act on — the vocabulary was wrong, ask
	// differently — instead of a shrug. And never an answer assembled from
	// whatever happened to be in context.
	db := gatherDB(t)
	c := twoStepUpstream(t, appleTVReply, "Ich vermute ...")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}))

	got, err := p.Run(context.Background(), "Wie laeuft der Versand?", AudienceBA, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(got.Text, "nichts gefunden") {
		t.Errorf("text = %q", got.Text)
	}
	if !strings.Contains(strings.ToLower(got.Text), "airplay") {
		t.Errorf("text = %q, want the tried terms named", got.Text)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none", got.Citations)
	}
}

func TestPipeline_reportsEveryStepInOrder(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")
	c := twoStepUpstream(t, appleTVReply, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}))
	var steps []string

	if _, err := p.Run(context.Background(), "Wie?", AudienceBA,
		Events{OnStatus: func(s string) { steps = append(steps, s) }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"verstehen", "suchen", "sammeln", "antworten"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
}
