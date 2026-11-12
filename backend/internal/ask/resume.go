package ask

// EntryRepos is what one card entry searches when it is chosen: the
// repositories a project entry folded, or the single repository it named, or
// none for the "all repositories" entry.
//
// Read from the RECORD, never resolved again. A project can gain a member in
// repos.yaml between the card being drawn and the button being pressed, and
// re-resolving would answer from a repository the card never offered — widening
// the reader's own choice behind their back.
//
// Empty members with a non-empty repo is a card stored before projects
// shipped, or a project of one. Both mean the same thing, and both are what
// that entry meant when it was drawn.
func EntryRepos(members []string, repo string) []string {
	if len(members) > 0 {
		return members
	}
	if repo != "" {
		return []string{repo}
	}
	return nil
}

// ResumeUnder is the scope and thread a turn resumed off a card runs under.
// One function for the handler and the eval harness, so a measured card
// choice cannot drift from the one a reader makes.
//
// stored is the scope the card's turn was asked under, as the record reads it
// back. It carries over: without it a resumed turn re-answers "how do loom and
// rongo differ" from rongo-only sources with no rule saying loom is not
// indexed, and the model writes loom's side out of its own training.
//
// repoChoice is a card whose entries were repositories or projects, or the
// too-broad panel; repos are the ones chosen, empty for the "all repositories"
// entry. The choice IS the scope then. Unknown carries over untouched: a
// repository the question named that the index does not have is still said
// out loud, whichever entry was picked. An empty choice is the reader's own
// permission to answer across the corpus and is recorded as such.
//
// prior is what earlier turns of the thread left behind. Its narrowing gives
// way to the card's: the choice a reader made on the card is what this turn is
// about, and the thread's older scope must not widen or contradict it. All is
// never carried into a resume: it belongs to a retry of the turn it was given
// in.
func ResumeUnder(stored Scope, repoChoice bool, repos []string, prior Thread) (Scope, Thread) {
	scope := stored
	if repoChoice {
		scope.All = len(repos) == 0
		scope.Known = repos
	}
	prior.Pin = scope.Known
	prior.All = false
	return scope, prior
}
