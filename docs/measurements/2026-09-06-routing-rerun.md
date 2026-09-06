# Routing on the current ladder and the 65-question set

Every routing table on record was measured on a ladder that no longer exists.
`2026-08-20-corpus-swap.md` and `2026-08-19-candidates.md` predate the
repository rung (#80), the `namedRepos == 1` early exit, and the Analyst role
gate (#61) - and they were measured over 60 or 61 questions against the 65 in
`questions.json` today. `route.go`'s own `Decide` comment says the sweep has to
be re-run and the shift written down. This is that run.

The question it has to settle: **is the ask-back ladder earning its keep, and
if not, is the loss in the decision or in the candidates it decides over?**
That second half is what the Embabel/GOAP comparison turns on - a planner or an
agentic loop changes step *selection*, so it can only help if the decision is
where the loss is.

## The corpus is pinned, and it reproduces exactly

The published tables were measured on a corpus that has since moved: master is
71 commits and ~19700 insertions ahead of where it stood, and rongo indexes
itself, so a third of the corpus *is* that diff. Comparing a new routing number
against the old table across that gap would confound a ladder change with a
corpus change, and neither would be attributable.

So all three repositories are pinned to the commit each stood at when
`2026-08-20-corpus-swap.md` was written, served from local clones carrying a
`pin20260820` branch (`repos.yaml` in this worktree, not the shipping file):

| repository | pinned to | when |
|---|---|---|
| rongo | `0b989b6` | 2026-08-20 06:55 - master at the time; `eee6150`, the commit recording that run, is its direct child |
| peeq | `bb04021` | 2026-08-20 06:48 |
| go-sqlite3 | `3fa3f30` | 2026-08-20 15:05 BST, the last commit before the run |

Two checks made the pin safe and then proved it:

- **No question breaks at the pin.** Every path named by all 65 questions
  exists at `0b989b6`, including the three doc-led questions added
  2026-09-05. The pin costs the catalogue nothing.
- **The index reproduces the published counts exactly**, which is a far
  stronger control than a recall number landing "within one question":

| repository | published 2026-08-20 | this run |
|---|---|---|
| peeq | 526 files / 5694 chunks | **526 / 5694** |
| rongo | 142 / 1358 | **142 / 1358** |
| go-sqlite3 | 293 / 2180 | **293 / 2180** |

Criterion 1 below is therefore settled outright: the corpus is not merely
comparable, it is the same one. Indexing took 291 s.

For the record, the raw-question control arm (`TestEvalMeasure`, which searches
the unexpanded question and so is not the 0.769 expansion figure) reports
`unique` recall@20 = 0.786 (33/42) and recall@5 = 0.714, MRR 0.552 over the
now-42-question `unique` cohort.

## The expansions are pinned too

`expansions.json` held 60 entries against 65 questions, and every routing arm
fatals on a missing one. `TestExpandQuestions` re-expands *all* questions and
overwrites the file wholesale - the frozen record survives only when the
model's three attempts fail. Re-freezing to add five questions would therefore
have regenerated the other sixty from a model the arm's own comment calls
non-deterministic, putting back exactly the confound the corpus pin removes.

This run adds an opt-in gate, `BACKEND_EVAL_EXPAND=missing`
(`expansion_test.go`, `expandOnlyMissing`): a question that already has a
frozen expansion keeps it byte for byte, and the model is called only for the
ones that have none. The full sweep stays the default, because a question whose
*wording* changed keeps a stale expansion under the new setting and only a full
sweep would notice. `TestExpandOnlyMissingIsOptInAndExact` runs without an
endpoint and pins the gate to the exact string.

## Criteria, fixed before the run

Written before any number landed, the way phase 4c fixed its criteria.

1. **Control.** `TestEvalMeasure` `unique` recall@20 must land within roughly
   one question of **0.769** (30/39), the value both `2026-08-20-corpus-swap.md`
   and `2026-08-22-repo-diversity.md` report to three decimals. Inside that,
   the old tables are comparable and this document may compare against them.
   Outside it, the corpus has drifted too far and every number here is reported
   standalone.

   The drift is known exactly, because `repos.yaml` clones rongo from GitHub
   master and the old runs are dated. The corpus-swap run indexed master at
   **`0b989b6`** (2026-08-20 06:55; the run is recorded one commit later in
   `eee6150`), and the diversity run indexed **`578bec7`** (2026-08-22 10:30).
   Master today is 71 commits ahead of `0b989b6`, with 138 files and 19737
   insertions across `backend/internal` and `route.go` alone grown by 718
   lines. rongo indexes itself, so a third of the corpus is that diff, and the
   ambiguous cohort asks about it. If the control misses, pinning rongo to
   `0b989b6` in `repos.yaml` is the like-for-like fallback - at the price of
   measuring the current ladder against code it was not built for.
2. **Does the ladder beat doing nothing?** Overall accuracy against the *never
   ask* baseline, which was **0.733 (44/60)** against the ladder's 0.633 (38/60)
   on the old rungs. The ladder has to clear *never ask* to be worth its cost.
   The criterion is a clear margin, not a tie: at least three questions.
3. **Where does the ambiguous cohort fail?** For every `ambiguous` question the
   router did *not* ask about, which rung decided. `route.go:566`'s rungs are
   `named_repos`, `all_repos`, `repo_deps`, `repository`, `margin`, `judge`,
   `role`, and each points at a different culprit:
   - `margin` with two or more candidates -> the decision. `Dominates` called a
     real ambiguity a clear winner.
   - `judge` -> the decision, and specifically the model's call.
   - `repository` reached but not fired, or a candidate list of one -> the
     candidates. Both alternatives were retrieved (12 of 16 on this corpus,
     `2026-08-22-repo-diversity.md:44`) but did not survive grouping,
     `worthOffering`'s relative floor, or the cap into two distinct candidates.
   - `role` -> the Analyst gate refused a card the judge asked for. Never
     measured before: every published routing number is the Developer path.
4. **Judge deployment.** Pro against ShortGate on the current set. The last
   pinned comparison (`2026-08-19-candidates.md:111`) had Pro ahead by six to
   seven questions on the *old* loom corpus; the corpus swap left a gap of one.
   A gap of at most one question is not a reason to keep the expensive lane.
5. **What a retry could be worth at most.** The grounding arm's not-asked
   number bounds it on the `unique` cohort: a retrieval-retry can only recover
   questions whose sources were missing, so the residual `1 - grounding` is the
   ceiling there. It is a ceiling for `unique` only - those questions have one
   expected file, so grounding cannot see a partly gathered answer. Composition
   (1 of 5, four of five missing the *same* first candidate) is where a retry
   would have to earn its keep, and that is `TestEvalMeasureGathered`'s arm,
   not one this run measures.

## Method

`hack/run-eval.sh <arm>` in a clean worktree, `BACKEND_EVAL=1`,
`BACKEND_EVAL_DB=/tmp/rongo-eval-small.db`, `BACKEND_REPO_ROOT=/tmp/rongo-eval-repos`.
The previous corpus and database were gone, so this is a full re-clone and
re-embed with no embedding-cache hits. Corpus is unchanged in shape: peeq,
rongo, go-sqlite3 (`repos.yaml`), `text-embedding-3-small` at 1536.

Arms, in order:

1. `TestEvalIndex` - build the corpus.
2. `TestEvalMeasure` - the control (criterion 1).
3. `TestExpandQuestions` - re-freeze `expansions.json`, 60 entries to 65.
4. `TestEvalMeasureRouting` - the ladder, judge on Pro and on ShortGate.
5. `TestEvalMeasureRoutingMarginSweep` - six margins, 0.10 to 0.40.
6. `TestEvalMeasureRoutingGrounding` - criterion 5.

## Results

### Routing: the ambiguous cohort is fixed, and the bill is 16 false cards

| | 2026-08-20, old ladder (n=60) | this run, current ladder (n=65) |
|---|---|---|
| overall, judge on Pro | 0.633 (38/60) | **0.723 (47/65)** |
| overall, judge on ShortGate | 0.617 (37/60) | **0.723 (47/65)** |
| `ambiguous` (want ask) | 0.062 (1/16) | **0.875 (14/16)** |
| `unique`+`composition`+`comparison` | 0.841 (37/44) | 0.673 (33/49) |
| *never ask* baseline | 0.733 (44/60) | 0.754 (49/65) |

The collapse the corpus-swap document recorded is gone: `ambiguous` went from
one question in sixteen to fourteen. It was bought with sixteen cards put to
the reader on questions that have exactly one answer, and the ladder therefore
still sits just under *never ask* - 47/65 against 49/65. The gap was ten
questions on the old rungs and is two now.

### The judge is not the problem, and Pro is not worth paying for

The two arms are identical **on every one of the 65 questions**, not merely in
aggregate: a line-by-line diff of the two per-question tables is empty. And the
rung breakdown says why - the judge decides nothing that is wrong:

| rung | questions it got wrong |
|---|---|
| `repository` | **16** |
| `repo_deps` | 2 |
| `margin` | 0 |
| `judge` | **0** |
| `role` | 0 |

The judge ran, said `ask` and `compose`, and was right every time it settled a
question. The six-to-seven-question gap that moved the judge to Pro
(`2026-08-19-candidates.md:111`, `AGENTS.md:25`, `route.go:352-370`) was
measured on the loom corpus, which no longer exists. On this corpus the gap is
zero, twice over: same accuracy, same decisions.

The margin sweep says the same thing from the other side, and more absolutely
than the old run did - **flat at 47/65 across all six margins**, 0.10 to 0.40.
The old sweep at least moved by two questions. The threshold now decides
nothing at all.

### Gathering is perfect on every question the router answers

| | published | 2026-08-20 | this run |
|---|---|---|---|
| grounded, of the questions NOT asked about | 0.955 | 0.882 | **1.000 (26/26)** |
| grounded, all `unique` | - | 0.773 | 0.619 (26/42) |

Every `unique` question the router actually answers has its expected file among
the sources the answer is built from. The all-`unique` number is lower only
because an asked turn never gathers and counts as ungrounded - that is the
same 16 cards, counted again from the other end.

Read this as a `unique`-cohort number and nothing wider. A `unique` question
names one file, so a perfect score here means "the one file arrived", not "the
answer had everything it needed". Composition, where several parts must arrive
and 1 of 5 currently do, is a different arm.

## What this settles

**1. The binding constraint is neither retrieval nor the judge. It is one
deterministic rule.** `2026-08-20-corpus-swap.md` concluded "the binding
constraint has moved from routing to retrieval". That is no longer true, and
the repository rung is why: it fires on
`namedRepos == 0 && distinctRepos(all) >= 2` (`route.go:576`) - repository
*span*, with no test of whether the second repository's candidate is any good.
`worthOffering`'s floor is relative to the leader and the rung does not consult
it. So a peeq-only question ("In which format does yt-dlp expect the cookie
file?", "How does an Apple TV get at the media file?") gets a card because some
rongo chunk drifted into the fused twenty.

That is the entire remaining loss. Sixteen questions, one rule, no model in
sight.

**2. Move the judge back to the cheap lane.** Identical decisions on all 65
questions, and zero wrong decisions attributed to it. `AGENTS.md:25` currently
says "Don't 'restore' it to non-Pro" on the strength of a corpus that is gone;
that line and `route.go:352-370` need this run's numbers, not defending. Not
changed here - this document is the measurement, the change is a decision of
its own.

**3. A retrieval retry has no headroom on `unique`, and very little anywhere
else.** Criterion 5 said the ceiling on any retry design is `1 - grounding`.
On the 26 `unique` questions the router answers, grounding is **1.000**: the
expected file is in the sources every time, so no second search, no wider `K`
and no quality-shaped goal has anything to recover there.

That is a statement about the `unique` cohort only, and `unique` questions have
one expected file, so grounding cannot see a *partly* gathered answer. The
place where something is genuinely missing is composition: 1 of 5 on this
corpus, with the same first candidate absent in four of five
(`2026-08-22-repo-diversity.md:70-78`). This run does not measure it - that is
`TestEvalMeasureGathered`'s arm. So the honest ceiling on a retry is those five
questions, not sixty-five, and whether a retry recovers any of them is
unmeasured rather than answered.

**4. All 18 wrong decisions came from deterministic rules. The one model call
got none of them wrong.** Sixteen from `repository`, two from `repo_deps` -
and the `repo_deps` pair is the sharper half: the manifest edge said the two
repositories are related, so the ladder composed across them, when the question
had two genuine alternatives. A go.mod dependency is not evidence that two
implementations are one mechanism, and no model was consulted.

That is the exact inversion of the case for GOAP. The pitch is a deterministic
planner with the model reasoning safely inside actions; here the deterministic
layer is the entire failure surface and the model is flawless. A planner or an
LLM tool-calling loop changes which *step* runs next, and step selection is not
where a single one of these 18 questions was lost.

The hypothesis for the next run - and it is a hypothesis, not a fix - is a
condition on the repository rung: require the spanning candidate to clear an
absolute bar, or to survive `worthOffering`, before a card is raised. It owns
all sixteen false cards, and the rung itself is one PR old (#80), so its
behaviour has never been measured before today. It also deserves the caution
this ladder has earned: phase 4c set criteria in advance, measured a judge
prompt that looked obviously better, and could not show it earning its keep.
Same discipline applies here.

## Two things this run changed in the harness

- **`BACKEND_EVAL_EXPAND=missing`** (`expansion_test.go`). Opt-in incremental
  freezing, so a growing question set no longer costs comparability with every
  table measured before it. Sixty expansions came through byte-identical here;
  the diff on `expansions.json` is 40 insertions and no deletions.
- **A rung column in the routing arm** (`route_test.go`, `DecideWhy` in place
  of `Decide`). Production has logged the deciding rung per turn since #64 for
  exactly this reason; the measurement did not, and without it the aggregate
  cannot tell a deterministic card apart from a judgement. It is what turned
  "routing is wrong 18 times" into "one rule is wrong 16 times".

## Addendum, same day: a second run weakens "identical" to "within one"

The arm was run again while moving the judge to the cheap lane, on the same
pinned corpus and the same frozen expansions.

| run | judge on ShortGate | judge on Pro |
|---|---|---|
| first | 0.723 (47/65) | 0.723 (47/65), per-question diff empty |
| second | 0.723 (47/65) | **0.708 (46/65)**, 1 wrong via `judge` |

The single difference is one `unique` question - "How does the download queue
tell that nobody is waiting for a job?" - which Pro carded and ShortGate
answered. The rung breakdown is otherwise unchanged in both runs: 16 wrong via
`repository`, 2 via `repo_deps`.

So the correct claim is **within one question of each other, in both
directions**, not "identical". The conclusion is unaffected and if anything
firmer - Pro is not more right, and this run it was one question less so - but
the stronger wording above belongs to the first run only.

The second finding is the more useful one: **`gateTemperature` narrows the
judge's re-roll, it does not remove it.** Phase 4c pinned the temperature
because two unpinned runs differed by three of 61 questions, which was wider
than the deployment gap being published. Pinned, two runs differ by one. That
is enough to tell a six-question gap from noise, which is what phase 4c needed,
and it is *not* enough to read a one-question gap as a result. Any future lane
or prompt comparison needs at least two runs before a single question means
anything.

## Caveats

- The Analyst role gate is still unmeasured. Every number here passes
  `roleCanChoose = true`, so this is the Developer path, as every published
  routing number has been.
- `comparison` is n=2 and `composition` n=5. Neither moves an aggregate
  meaningfully, and the composition cohort's own weakness (1 of 5, same first
  candidate missing) is a gathering question this arm does not address.
- The corpus is pinned to 2026-08-20 on purpose. It says what the ladder does
  to the *old* catalogue, which is the only way the comparison is honest. What
  the ladder does to today's code is a different run, and it needs today's
  corpus and a re-frozen expansion set to go with it.
