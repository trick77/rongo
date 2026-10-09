package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errTestMiss = errors.New("miss")

// testView is a serveCited over strings: read fails with what the test says,
// the record holds "recorded", and the table maps errTestMiss to 404.
func testView(readErr error) citedView[string] {
	return citedView[string]{
		read:         func(context.Context) (string, error) { return "", readErr },
		readRecorded: func(context.Context) (string, error) { return "recorded", nil },
		fromRecord:   func(err error) bool { return errors.Is(err, errTestMiss) },
		citationLog:  "read citation failed",
		readLog:      "read failed",
		statuses: []errStatus{
			{err: errTestMiss, status: http.StatusNotFound, body: "This is not here."},
		},
	}
}

func TestServeCited(t *testing.T) {
	cases := []struct {
		name   string
		readE  error
		cited  citedFunc
		status int
		body   string
	}{
		{"a listed error is its row", fmt.Errorf("%w: x", errTestMiss), nil, http.StatusNotFound, "This is not here."},
		{"an unlisted error is 500", errors.New("disk I/O error"), nil, http.StatusInternalServerError, "internal server error"},
		{"a cited miss is read from the record", errTestMiss, alwaysCited, http.StatusOK, `"recorded"`},
		{"an uncited miss stays a miss", errTestMiss, func(context.Context) (bool, error) { return false, nil }, http.StatusNotFound, "This is not here."},
		{"a citation check that fails is 500", errTestMiss, func(context.Context) (bool, error) { return false, errors.New("locked") }, http.StatusInternalServerError, "internal server error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			serveCited(rec, httptest.NewRequest(http.MethodGet, "/", nil), c.cited, testView(c.readE))
			if rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.body {
				t.Errorf("got %d %q, want %d %q", rec.Code, rec.Body.String(), c.status, c.body)
			}
		})
	}
}

func TestServeCited_aMissOutsideTheRuleIsNeverReadFromTheRecord(t *testing.T) {
	// The source route asks the record only for a miss WITH a sha; the rule
	// is the view's, and the record stays shut when it says no.
	v := testView(errTestMiss)
	v.fromRecord = func(error) bool { return false }
	rec := httptest.NewRecorder()

	serveCited(rec, httptest.NewRequest(http.MethodGet, "/", nil), alwaysCited, v)

	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}
