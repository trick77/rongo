package indexer

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
)

// InventoryAttrs describes one repository for the startup log, as slog
// key/value pairs.
//
// It exists because "repository list loaded, entries=6" was the whole of what
// a boot said: not which repositories, not which were parked, not which had
// been indexed and at what. Every question a support conversation opens with —
// is it configured, is it enabled, has it ever indexed, when, and did the last
// run fail — had to be answered by reading the YAML and the database by hand,
// on a machine somebody else operates.
//
// Empty fields are LEFT OUT rather than printed blank. A repository that has
// never indexed says nothing about a sha instead of `indexed_sha=""`, so the
// eye can tell "no value" from "value that happens to be empty" without
// counting quotes, and a healthy line stays short enough to read at a glance.
func InventoryAttrs(st RepoState) []any {
	source := st.CloneURL
	if st.Snapshot() {
		// The clone_url is empty for a snapshot, which is exactly what makes it
		// one; printing nothing there would read as a missing configuration.
		source = "snapshot"
	}
	attrs := []any{
		"repo", st.Name,
		"project", st.Project,
		"source", source,
		"enabled", st.Enabled,
	}
	if st.Part != "" {
		attrs = append(attrs, "part", st.Part)
	}
	if st.Branch != "" {
		attrs = append(attrs, "branch", st.Branch)
	}
	if st.LastSHA != "" {
		attrs = append(attrs, "indexed_sha", gitrepo.ShortSHA(st.LastSHA),
			"files", st.Files, "chunks", st.Chunks)
	} else {
		// Said out loud rather than implied by absent counters. A repository
		// that has never indexed is the single most common cause of "rongo
		// cannot find anything in X", and it must not look like one that
		// indexed zero files.
		attrs = append(attrs, "indexed", false)
	}
	if !st.LastRunAt.IsZero() {
		attrs = append(attrs, "last_run_at", st.LastRunAt.UTC().Format(time.RFC3339))
	}
	if st.LastError != "" {
		attrs = append(attrs, "last_error", st.LastError)
	}
	if len(st.Uses) > 0 {
		// The declared edges, because they are the one part of a repository's
		// configuration with no other way to check it took effect. repos.yaml
		// is read once, at boot; an operator who edits a `uses` line and
		// restarts has nothing but the arrow on the Projects page to tell them
		// whether the file was picked up — and if the parse failed, that arrow
		// is the PREVIOUS configuration's, which reads as the direction being
		// drawn backwards rather than as the file never being loaded.
		attrs = append(attrs, "uses", strings.Join(st.Uses, ","))
	}
	return attrs
}

// LogInventory writes what rongo actually holds: one line per repository, then
// one summary line.
//
// It lives here rather than in main so it can be tested — nothing verified that
// a boot announced its corpus at all, and the boot where that matters most is
// the one where the repository list did NOT load, which is precisely the path a
// main-only implementation gets wrong quietly.
//
// fromFile says whether what follows came from the list on disk. False means the
// database is describing a configuration that is no longer anywhere, which is a
// different sentence and gets one.
func LogInventory(ctx context.Context, s *StateStore, log *slog.Logger, path string, fromFile bool) {
	if log == nil {
		log = slog.Default()
	}
	states, err := s.All(ctx)
	if err != nil {
		log.Warn("repository inventory unavailable", "err", err)
		return
	}
	for _, st := range states {
		log.Info("repository configured", InventoryAttrs(st)...)
	}
	msg := "repository list loaded"
	if !fromFile {
		msg = "serving a corpus no repository list describes"
	}
	log.Info(msg, append([]any{"path", path, "from_file", fromFile},
		Summarise(states).Attrs()...)...)
}

// Inventory counts a corpus for the one summary line that follows the per-
// repository ones, so a long list still ends in a number somebody can check
// against what they expect to be configured.
type Inventory struct {
	Projects  int
	Repos     int
	Enabled   int
	Parked    int
	Snapshots int
	Indexed   int
	Failing   int
}

// Summarise counts states the way an operator reads them: how much is
// configured, how much of it is live, and how much of the live part is actually
// answering questions.
func Summarise(states []RepoState) Inventory {
	inv := Inventory{Repos: len(states)}
	projects := map[string]bool{}
	for _, st := range states {
		// The same fallback the Projects page applies: a row written before
		// projects shipped stands as a project of its own, so the count here
		// and the count on the page cannot disagree.
		project := st.Project
		if project == "" {
			project = st.Name
		}
		projects[project] = true

		if st.Enabled {
			inv.Enabled++
		} else {
			inv.Parked++
		}
		if st.Snapshot() {
			inv.Snapshots++
		}
		if st.LastSHA != "" {
			inv.Indexed++
		}
		if st.LastError != "" {
			inv.Failing++
		}
	}
	inv.Projects = len(projects)
	return inv
}

// Attrs renders the summary as slog key/value pairs. Parked, snapshots and
// failing are omitted at zero: a corpus with none of them should not carry
// three zeros on every boot, and a non-zero one is worth noticing.
func (i Inventory) Attrs() []any {
	attrs := []any{
		"projects", i.Projects,
		"repositories", i.Repos,
		"enabled", i.Enabled,
		"indexed", i.Indexed,
	}
	if i.Parked > 0 {
		attrs = append(attrs, "parked", i.Parked)
	}
	if i.Snapshots > 0 {
		attrs = append(attrs, "snapshots", i.Snapshots)
	}
	if i.Failing > 0 {
		attrs = append(attrs, "failing", i.Failing)
	}
	return attrs
}
