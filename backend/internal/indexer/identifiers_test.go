package indexer

import (
	"reflect"
	"testing"
)

func TestSplitIdentifiers_splitsTheFourBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"camelCase", "class AbandonedCartJob {", []string{"abandoned", "cart", "job"}},
		{"snake_case", "def estimate_tokens(s):", []string{"estimate", "tokens"}},
		{"an acronym keeps its head", "new HTTPServer()", []string{"http", "server"}},
		{"letters and digits", "sha256Sum(x)", []string{"256", "sha", "sum"}},
		{"lowerCamel", "promoMailer.send()", []string{"mailer", "promo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitIdentifiers(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitIdentifiers(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitIdentifiers_skipsWhatTheLaneAlreadyHas(t *testing.T) {
	// A single-word identifier is already a token of raw_text — repeating it in
	// aux would only weight the lane towards files that hold many of them. So
	// is anything unicode61 splits by itself: `_`, `.`, `-` and `/` are
	// separators to it already, and only the case and digit boundaries are new.
	if got := splitIdentifiers("sender.send(); run(); a - b"); len(got) != 0 {
		t.Errorf("splitIdentifiers(...) = %v, want nothing: every word is already its own token", got)
	}
	// A leftover of one or two runes off a boundary is not vocabulary anybody
	// asks in, and the identifier itself stays findable in raw_text.
	if got := splitIdentifiers("getX()"); !reflect.DeepEqual(got, []string{"get"}) {
		t.Errorf("splitIdentifiers(\"getX()\") = %v, want only the word long enough to be asked for", got)
	}
}

func TestSplitIdentifiers_dedupesAndSorts(t *testing.T) {
	// Sorted and deduplicated, so the stored text is a function of the chunk
	// alone: re-indexing unchanged code must write the same bytes.
	got := splitIdentifiers("cartJob(cart_job, JobCart)")
	if want := []string{"cart", "job"}; !reflect.DeepEqual(got, want) {
		t.Errorf("splitIdentifiers(...) = %v, want %v", got, want)
	}
}
