package indexer

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store/storetest"
)

// monthRemote is a remote on main with two month branches, each adding
// its own file: rel/2026-10.1 holds oct.txt, rel/2026-11.1 oct.txt and
// nov.txt.
func monthRemote(t *testing.T) string {
	t.Helper()
	src := gittest.Fixture(t)
	gittest.Run(t, src, "checkout", "-q", "-b", "rel/2026-10.1")
	gittest.Commit(t, src, "oct.txt", []byte("october\n"), "october")
	gittest.Run(t, src, "checkout", "-q", "-b", "rel/2026-11.1")
	gittest.Commit(t, src, "nov.txt", []byte("november\n"), "november")
	gittest.Run(t, src, "checkout", "-q", "main")
	return src
}

// patternPoller is a poller for one pattern entry, letters, at the clock
// *now reads. The caller moves *now between polls.
type patternPoller struct {
	p   *Poller
	s   *StateStore
	db  *sql.DB
	rec *recordingIndex
	log *capture
	now time.Time
}

func newPatternPoller(t *testing.T, src, pattern string, switchDay int) *patternPoller {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	bp, err := repos.ParseBranchPattern(pattern, switchDay)
	if err != nil {
		t.Fatal(err)
	}
	specs := []repos.Spec{{Name: "letters", CloneURL: src, BranchPattern: bp, Enabled: true}}
	db := storetest.Open(t, 1536)
	s := NewStateStore(db)
	if _, err := s.SyncSpecs(context.Background(), specs); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	pp := &patternPoller{s: s, db: db, rec: &recordingIndex{}, log: &capture{}}
	pp.p = NewPoller(PollerDeps{
		State:    s,
		Git:      gitrepo.New(gitBin, t.TempDir()),
		Index:    pp.rec.fn,
		Patterns: PatternsOf(specs),
		Now:      func() time.Time { return pp.now },
		Logger:   slog.New(pp.log),
	})
	return pp
}

func (pp *patternPoller) poll(t *testing.T, now time.Time) {
	t.Helper()
	pp.now = now
	if err := pp.p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}
}

func (pp *patternPoller) row(t *testing.T) (branch, sha, lastErr string) {
	t.Helper()
	if err := pp.db.QueryRow(`SELECT branch, last_sha, last_error FROM repo_state WHERE name = 'letters'`).
		Scan(&branch, &sha, &lastErr); err != nil {
		t.Fatal(err)
	}
	return branch, sha, lastErr
}

func TestPollOnce_aPatternEntryResolvesOnTheFirstClone(t *testing.T) {
	// Given: a never-cloned pattern entry, and a remote that also serves a
	// branch literally named HEAD, which no pattern can claim.
	src := monthRemote(t)
	nov := gittest.Run(t, src, "rev-parse", "rel/2026-11.1")
	gittest.Run(t, src, "update-ref", "refs/heads/HEAD", nov)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 25)

	// When: on the switch day.
	pp.poll(t, day(2026, 11, 25))

	// Then: November's branch, in full, recorded as what is indexed.
	branch, sha, lastErr := pp.row(t)
	if branch != "rel/2026-11.1" || sha != nov || lastErr != "" {
		t.Errorf("row = %q, %q, %q; want rel/2026-11.1 at %s, no error", branch, sha, lastErr, nov)
	}
	if len(pp.rec.calls) != 1 || pp.rec.calls[0].Paths != nil {
		t.Errorf("index calls = %+v, want one full run", pp.rec.calls)
	}
}

func TestPollOnce_aMonthChangeSwitchesTheBranchIncrementally(t *testing.T) {
	// Given: indexed on October's branch the day before the switch.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 25)
	pp.poll(t, day(2026, 11, 24))
	if branch, _, _ := pp.row(t); branch != "rel/2026-10.1" {
		t.Fatalf("branch = %q before the switch day, want rel/2026-10.1", branch)
	}

	// When: the switch day arrives.
	pp.poll(t, day(2026, 11, 25))

	// Then: November's branch, and only the file it adds.
	branch, sha, _ := pp.row(t)
	if want := gittest.Run(t, src, "rev-parse", "rel/2026-11.1"); branch != "rel/2026-11.1" || sha != want {
		t.Errorf("row = %q at %s, want rel/2026-11.1 at %s", branch, sha, want)
	}
	if len(pp.rec.calls) != 2 {
		t.Fatalf("index calls = %+v, want two", pp.rec.calls)
	}
	if got := pp.rec.calls[1].Paths; len(got) != 1 || got[0] != "nov.txt" {
		t.Errorf("Paths = %v, want exactly [nov.txt]: a month change is an incremental branch change", got)
	}
	logged := false
	for _, r := range pp.log.records {
		if r.Message == "branch pattern resolved" && attrText(r, "branch") == "rel/2026-11.1" &&
			attrText(r, "previous") == "rel/2026-10.1" {
			logged = true
		}
	}
	if !logged {
		t.Error("no resolution logged from October's branch to November's")
	}

	// When: the same month polled again.
	pp.poll(t, day(2026, 11, 26))

	// Then: nothing to do.
	if len(pp.rec.calls) != 2 {
		t.Errorf("index calls = %d, want the unchanged month left alone", len(pp.rec.calls))
	}
}

func TestPollOnce_aPatternMatchingNothingKeepsTheIndex(t *testing.T) {
	// Given: indexed on November's branch.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 0)
	pp.poll(t, day(2026, 11, 15))
	_, before, _ := pp.row(t)

	// When: the month branches are gone upstream.
	gittest.Run(t, src, "branch", "-q", "-D", "rel/2026-10.1", "rel/2026-11.1")
	pp.poll(t, day(2026, 11, 16))

	// Then: an error on the page, never the default branch, index as it was.
	branch, sha, lastErr := pp.row(t)
	if branch != "rel/2026-11.1" || sha != before {
		t.Errorf("row = %q at %s, want rel/2026-11.1 at %s left as it was", branch, sha, before)
	}
	if !strings.Contains(lastErr, "rel/{yyyy}-{mm}.{n}") {
		t.Errorf("last_error = %q, want the pattern named", lastErr)
	}
	if len(pp.rec.calls) != 1 {
		t.Errorf("index calls = %d, want no run without a match", len(pp.rec.calls))
	}
}

func TestPollOnce_aFreshPatternEntryNeverFallsBackToTheDefaultBranch(t *testing.T) {
	// Given: only a future month exists.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 0)

	// When
	pp.poll(t, day(2026, 9, 15))

	// Then: no branch recorded, nothing indexed, the reason on the page.
	branch, sha, lastErr := pp.row(t)
	if branch != "" || sha != "" || !strings.Contains(lastErr, "2026-09") {
		t.Errorf("row = %q, %q, %q; want nothing indexed and the target month named", branch, sha, lastErr)
	}
	if len(pp.rec.calls) != 0 {
		t.Errorf("index calls = %+v, want none", pp.rec.calls)
	}
}

func TestPollOnce_aSkippedMonthIndexesTheOlderOneAndSaysSo(t *testing.T) {
	// Given: no December branch.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 0)

	// When
	pp.poll(t, day(2026, 12, 3))

	// Then
	if branch, _, lastErr := pp.row(t); branch != "rel/2026-11.1" || lastErr != "" {
		t.Errorf("row = %q, %q; want rel/2026-11.1 and no error", branch, lastErr)
	}
	r, ok := pp.log.find("branch pattern matched an older month")
	if !ok {
		t.Fatal("no log line for the older month")
	}
	if attrText(r, "target_month") != "2026-12" || attrText(r, "branch") != "rel/2026-11.1" {
		t.Errorf("log attrs target_month=%q branch=%q, want 2026-12 and rel/2026-11.1",
			attrText(r, "target_month"), attrText(r, "branch"))
	}
}

func TestPollOnce_aFailedMonthChangeKeepsTheOldBranchOnTheRow(t *testing.T) {
	// Given: indexed on October's branch.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 25)
	pp.poll(t, day(2026, 11, 24))
	_, oct, _ := pp.row(t)

	// When: the switch day arrives and the index for November fails.
	pp.rec.err = errors.New("embeddings down")
	pp.poll(t, day(2026, 11, 25))

	// Then: the row still names the branch its chunks came from.
	if branch, sha, _ := pp.row(t); branch != "rel/2026-10.1" || sha != oct {
		t.Errorf("row = %q at %s, want rel/2026-10.1 at %s until November is indexed", branch, sha, oct)
	}

	// When: the next poll succeeds.
	pp.rec.err = nil
	pp.poll(t, day(2026, 11, 25))

	// Then
	if branch, _, _ := pp.row(t); branch != "rel/2026-11.1" {
		t.Errorf("branch = %q, want rel/2026-11.1 once its index stands", branch)
	}
}

func TestPollOnce_anOlderMonthIsLoggedOncePerMonth(t *testing.T) {
	// Given: no December branch.
	src := monthRemote(t)
	pp := newPatternPoller(t, src, "rel/{yyyy}-{mm}.{n}", 0)

	// When: polled three times in December.
	pp.poll(t, day(2026, 12, 3))
	pp.poll(t, day(2026, 12, 4))
	pp.poll(t, day(2026, 12, 5))

	// Then
	n := 0
	for _, r := range pp.log.records {
		if r.Message == "branch pattern matched an older month" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("older-month warnings = %d, want one for the month", n)
	}
}

func TestPatternsOf_keepsOnlyPatternEntries(t *testing.T) {
	bp, err := repos.ParseBranchPattern("rel/{yyyy}-{mm}", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := PatternsOf([]repos.Spec{{Name: "letters", BranchPattern: bp}, {Name: "plain", Branch: "main"}})
	if len(got) != 1 || got["letters"] != bp {
		t.Errorf("PatternsOf() = %v, want letters alone", got)
	}
}

// attrText is a record's attribute as a string, "" when absent.
func attrText(r slog.Record, key string) string {
	v, ok := attr(r, key)
	if !ok {
		return ""
	}
	return v.String()
}
