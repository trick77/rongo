# The candidate layer — and the measurement that could not see itself

**In short.** The candidate change did not beat the target and is not shipped. What did land
came from fixing the measurement: the harness had been measuring a router the product does not
run, an invented repository name was wiping searches in production, and the routing judge was
re-rolling three of sixty-one decisions per run because no call carried a temperature. Pinning
that last one showed the phase 4b conclusion to be an artefact — the judge is six to seven
questions better on Pro, and now runs there. The router's best measured configuration is
50/61 (0.820), with its other sample at 48/61: by the residual spread this document itself
establishes, that is *on* the 0.803 line rather than past it. The movement came from removing
noise, not from improving the candidates.


Phase 4b ended with a number to beat: a router that **never asks** scores **0.803 (49/61)**
on the question catalogue, and every configuration measured up to then was worse. This
document is phase 4c's attempt on that number.

It does not begin with the candidates. It begins with two defects in how the routing was
being measured at all, because both were large enough to change what the earlier numbers
mean, and one of them overturns a finding the phase 4b spec made mandatory.

Same corpus as the 2026-08-18 document, `text-embedding-3-small` at 1536 dimensions,
`/tmp/rongo-eval-small.db`, unchanged between every arm below.

## The anchor holds

| anchor: the original 28, first candidate only | measured | published |
|---|---|---|
| recall@5 | **0.679** | 0.679 |
| recall@20 | **0.786** | 0.786 |
| MRR | **0.476** | 0.476 |

The 44-question `unique` cohort reproduces its control too: recall@20 = 0.750. Every number
below is comparable with phases 2, 3, 4a and 4b.

**0.803 does not move.** It is 49 of 61 questions that want no clarification — a property of
the catalogue's composition, not of retrieval. Nothing in this phase can shift the target,
only the router's distance from it.

## Defect one: the harness measured a router the product does not run

`pipeline.go:93` searches with `Repos: u.Repos` — the understanding step narrows the corpus
whenever the question names a system. The frozen `expansions.json` recorded only the search
texts, and the routing arms searched without the restriction. Every routing row ever
published therefore came from a candidate set production would not have produced: peeq's
twin in loom appearing beside it for a question that said "peeq".

Nine of the 44 `unique` and one of the 12 `ambiguous` questions name a repository outright.

The fix is in the harness, not the product: `expansion` records gained an optional `repos`
field, frozen by an arm of its own so the texts stay byte-identical and the phase 3 and 4a
documents keep their meaning.

The same defect had a second instance, already known and now closed: the arms carried their
own copy of the routing ladder (`askAt`), so a change to `Route`'s rung order would have left
them compiling while measuring the previous policy. There is now one `ask.Decide`, called by
`Route` and by the sweep alike.

## Defect two: an invented repository name wipes the search

Freezing the restriction made it readable, and it is not clean. Of the nine questions that
produce one:

| question | the model named | the index has |
|---|---|---|
| „…translated into **peeqs** own values?" | `peeqs` | `peeq` — the possessive read as a name |
| „Which SponsorBlock categories does **peeq** store…" | `Peek` | `peeq` |
| „…so that **sqlite-vec** accepts it?" | `asg017/sqlite-vec` | not indexed at all |

`retrieve` put these straight into `WHERE f.repo IN (…)`. A name nothing carries is not a
narrowing, it is a wipe: no row can match, and `pipeline.go` then reports "nothing found"
for a question whose answer is sitting in the index. **One third of all narrowings did
this**, in production, silently.

`Retriever.Search` now drops names the index does not know, and a restriction left with
nothing becomes no restriction — the behaviour from before the field existed. A name the
index *does* know still restricts, empty result and all: "no hit means no hit" stays true for
a repository that really is empty.

## Defect three: the judge was re-rolling its decisions

With the harness corrected, the routing arm was run twice over identical frozen inputs and an
unchanged corpus. It gave different answers:

| judge on Pro | run A | run B |
|---|---|---|
| overall | 0.672 (41/61) | 0.721 (44/61) |
| `ambiguous` | 5/12 | 6/12 |
| `unique`+`composition` | 36/49 | 38/49 |

Nothing had changed between them. `internal/llm/client.go` sent no temperature on any
request, so every call sampled at the endpoint's default — including the gates whose entire
output is one word.

**Three questions of run-to-run noise, which is larger than the difference phase 4b
published between the two deployments.** The reader meets the same defect from the other
side: ask the same question twice and get a card once and an answer the other time.

`WithTemperature` now exists and pins the understanding, the routing judge, the candidate
naming and the thread title. The answer call is deliberately left alone — a person reads that
one, and pinning it to make a routing measurement reproducible would change what everybody
reads.

Pinning does not make a served endpoint deterministic, and the residual is visible: within a
single pinned run the comparison arm and the sweep arm score the same judge at the same
margin one question apart. **Residual spread: one to two questions.** Every comparison below
is read against that.

## The mandated finding from phase 4b was an artefact

The phase 4b spec required the routing judgement to be measured against Pro before non-Pro
was written in. It was, and the answer was "one question out of sixty-one" — inside the noise
that was not yet known to exist. Pinned, and run twice:

| | run 1 | run 2 |
|---|---|---|
| judge on ShortGate (non-Pro) | 0.689 (42/61) | 0.705 (43/61) |
| **judge on Pro** | **0.787 (48/61)** | **0.820 (50/61)** |
| ShortGate `ambiguous` | 6/12 | 5/12 |
| Pro `ambiguous` | 7/12 | 7/12 |

Six and seven questions apart, against a residual of one to two. The gap is real, and it is
the opposite of what was concluded.

The routing judge therefore moves to Pro. This is the one exception to "Pro only where a
human reads", and it is recorded in `AGENTS.md` as such: the judge's output is a single word,
which is the bar for the cheap lane, but that word decides whether the reader gets an answer
or a question back. Understanding, naming and the thread title stay on the cheap lane, where
a wrong output costs a worse search or a worse title, not a wrong turn.

## Fixed criteria

Set before the candidate change was written, against the pinned baseline rather than the
divergent one:

- Overall **> 0.803**, and to count as a result rather than a re-roll, **≥ 51/61 (0.836)** —
  the target plus the residual spread.
- `ambiguous` must not fall below the pinned baseline on the lane being changed: **7/12** on
  Pro.
- Grounding over the not-asked `unique` cohort must not regress from **0.944**.

## What the router still got wrong

The eight questions Pro asked about that it should have answered:

| question | one answer, in |
|---|---|
| From when on does a video count as old material…? | `peeq` `scan/scheduler.go` |
| Why may the call for the key points spend more tokens…? | `peeq` `summarize/summarizer.go` |
| How do you pass a Go slice as a table…? | `go-sqlite3` `ext/array` |
| How many texts go into an embedding request at most? | `peeq` `rag/embed.go` |
| How many full-text hits does the message search return…? | `loom` `chat/message_search_store.go` |
| How many parked videos may a scan run re-check…? | `peeq` `scan/scheduler.go` |
| How is a file name sanitised…? | `loom` `artifact/path.go` |
| *(composition)* What happens on two concurrent writes? | `peeq` `store` + `go-sqlite3` |

Seven of eight are "how many / how much / from when": the answer is one constant in one file,
the question carries almost no lexical hook, and every module has limits and defaults. The
judge saw two excerpts that each contained a numeric constant and reasonably called them
alternatives.

One of them is worth singling out. `How do you pass a Go slice as a table` puts
`ext/array` against `ext/csv` — two modules of the **same** repository, so no repository
narrowing could ever have helped. What separates them is that the question's own expected
identifiers land on one and not the other, and the judge was never shown that.

## The change that did not land

The judge was asked a different question. Phase 4b's prompt asked whether the candidates were
alternatives, which two look-alike modules always are. The replacement asked whether the
*question* picks one of them out, and carried the three things that could answer that, none
of which costs a query or a model call because all three were already computed: the
repository the question named, how much of the search result each candidate holds and at what
rank, and which of the expected identifiers appear in each candidate's text.

Measured twice, against the fixed criteria:

| judge on Pro | baseline (runs 1/2) | loose rule | strict rule |
|---|---|---|---|
| overall | 48/61, 50/61 | 49/61 (0.803) | 50/61 (0.820) |
| `ambiguous` | 7/12, 7/12 | **4/12** | 7/12 |
| `unique`+`composition` | 41/49, 43/49 | **45/49** | 43/49 |
| `unique` questions asked about | 9 of 44 | **4 of 44** | 7 of 44 |
| grounding, not-asked cohort | 0.941 | 0.950 | 0.946 |

**Neither version earns its keep, and the two fail differently.**

The loose rule — ask only when *none* of the three signals distinguishes the candidates — is
the phase 4b document's warning made real. It halved the wrong clarifications, 8 down to 4,
and lifted grounding to the best figure yet measured. It also stopped asking about the
questions that genuinely need asking, 7/12 down to 4/12, and landed on exactly 0.803: it
matched the router that says nothing by becoming it. The evidence block almost always shows
*some* asymmetry — hit counts rarely tie, some term usually lands somewhere — so the model
nearly always found a reason to call the question decided.

The strict rule required the distinction to be decisive: the named repository matching exactly
one candidate, or expected identifiers landing on one and on none of the others, with hit
counts explicitly ruled out as a reason on their own. That restored the ambiguous cohort to
7/12 and scored 50/61 — which is precisely the baseline's better sample, on every one of the
three sub-numbers. It clears 0.803 and misses the ≥ 51/61 bar by one question, so it cannot be
told apart from changing nothing.

On the cheap lane the evidence is actively harmful: `ambiguous` falls to 1/12 (loose) and
2/12 (strict) against a baseline of 5–6/12.

**The judge's prompt is therefore back to phase 4b's wording and the evidence is not shipped.**
The criteria were fixed before the runs, and this is what they were for.

No further measurement was spent confirming the revert, because none is needed: the pinned
baseline's Pro arm ran exactly this configuration — phase 4b's prompt, `gateTemperature`, the
default deployment — and `NewRouter` now builds it again. The shipped router *is* the
configuration behind the 48/61 and 50/61 rows above.

What the phase learned rather than delivered: the evidence *is* a strong lever — 8 wrong
clarifications down to 4 is the largest single movement measured on that cohort — and the
unsolved part is the threshold, not the signal. A rule that separates "decisive" from "merely
asymmetric" is what the next attempt needs, and hit-share is probably not part of it: near-ties
are the only reason the ladder reached the judge in the first place.

## Also in this phase

**A citation pointed at the wrong repository.** `gather.go`'s reference walk followed symbol
*names* across repository boundaries with no preference for the source's own repository, so
an answer about peeq cited `loom/backend/internal/auth/session.go:129-141` although peeq has
its own byte-identical `randomToken` at `session.go:135-141`. The spec's condition — "the
gathered code really references the symbol" — was name-based, so the invariant was formally
met and materially broken: the reader following the citation lands in the other product.

Resolution now happens in the source's own repository first, and crosses a boundary only for
a name that repository does not define at all. Genuine composition over `repo_deps` edges
still travels.

Checked by hand against the running app, on the same round trip that produced it —
„How is a session token stored in the database?", card offered, peeq chosen:

    peeq backend/internal/auth/session.go:40-54
    peeq backend/internal/auth/session.go:55-85
    peeq backend/internal/auth/session.go:99-107
    peeq backend/internal/auth/session_test.go:31-61
    peeq backend/internal/store/migrations/0001_init.sql:66-115

Every source peeq, and the string `loom` does not occur anywhere in the response.

One thing the same hand-check showed that is NOT fixed: two of the three candidates on that
card were titled „Session-Speicherung in Datenbank" and „Session-Speicherung in Datenbank" —
the naming prompt asks for titles that tell two candidates apart and did not deliver here.
The repository is shown beside each, so the card is still answerable, but the titles are
carrying none of the load. Not touched this phase.

**Smaller things.** `excerptOf` cut at a byte offset and could split a UTF-8 rune, so a German
comment reached the model with a U+FFFD in it. `threads.Clarification` serialised the
`Understanding` to the browser, which never read it; the column stays as provenance and the
field is `json:"-"`.

## What this leaves open

The frozen repo restrictions were sampled before the temperature was pinned, and they stay
frozen — `Search` now makes the three bad names inert, so they cost a narrowing rather than an
answer. Re-freezing them would change the measurement's inputs for no gain this phase.

The `composition` cohort is unchanged and unanswerable for the reason phase 4b gave: the seam
is `sql.Open("sqlite3", …)` plus a blank import — a *string*, not a symbol — and the reference
walk does not cross strings.
