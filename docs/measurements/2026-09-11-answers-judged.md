# The answers, judged: a number for the answer itself

**Status: built and run 2026-09-11. `TestEvalMeasureAnswers` runs the product
pipeline end to end and grades every answer against a rubric; run on the
flow corpus before and after the reranker shipped, and on a private
two-repository estate (an nx workspace and a Maven multi-module build). This
is the number a model swap is judged by.**

## Why

Every measurement in this directory scores hits, gathered files or routing
decisions. None read an answer. Grounding at 1.000 says the right file was
in front of the model; it does not say the model stated the mechanism, or
that it stated only what the code does. And when the deployments change in a
few months — they will — nothing here could say whether the next model
answers better or worse.

## The arm

For each question the pipeline runs as the product runs it: understand,
search (reranked, since `2026-09-11-arms-after-the-crossing.md`), route,
gather, answer, Analyst voice, in the question's language. A rubric per
question, written from the verified code beside the question, lists the
claims a correct answer makes and the claims it must not make — the
confidently incomplete reading the flow corpus was built to expose. A
short-gate judge at temperature zero returns `present`, `absent` or
`contradicted` per claim and `asserted` or `absent` per forbidden claim;
citation coverage counts the question's parts among the answer's citations.
Two runs, reported side by side, because a judge re-rolls one or two
questions in sixty. Answers are written to a file for reading, because the
numbers say how much was right and the text says what was wrong.

`BACKEND_EVAL_PRO_MODEL` and `BACKEND_EVAL_GATE_MODEL` point the two lanes at
another deployment for the harness alone (`llm.Config.Pro`/`ShortGate`; the
product reads neither). Same corpus, same rubrics, another model: that is
the swap test.

## Flow corpus, ten questions

| arm | run | rubric present | contradicted | forbidden asserted | parts cited | asked back | upstream failures |
|---|---|---|---|---|---|---|---|
| fused order | 1 | 22/23 | 0 | 0 | 15/24 | 1 | 2 |
| fused order | 2 | 20/25 | 0 | 0 | 14/24 | 0 | 1 |
| **reranked (the product)** | 1 | **26/30** | 0 | 1 | 15/30 | 0 | 0 |
| **reranked (the product)** | 2 | **21/26** | 0 | 0 | 16/25 | 1 | 0 |

Denominators differ because a turn that asked back or failed upstream grades
nothing. Read across the four runs:

- **Nothing is contradicted, and the forbidden claims stay out** (one
  assertion in forty grades). The "never invent" rules hold on a corpus built
  to tempt them: no answer said the orders service decides the payment, or
  that a lost shipment is retried.
- **Rubric claims present sit at 84–96 %** on both arms; the reranker's
  runs are not distinguishable from the fused order's inside the judge's
  noise. What the reranker changed was what reached the answer
  (`2026-09-11-arms-after-the-crossing.md`); the answer's grade did not move
  with it on this corpus.
- **Citation coverage is the weak column: 15–16 of 24–30 parts.** The
  answers cite ten of 150–180 sources. The Analyst voice is told to explain
  the mechanism in three to five paragraphs and stop, and it cites what it
  quotes; a part that was read but not quoted is not cited. Whether that is a
  loss depends on what a citation is for, and this arm makes it visible
  rather than settling it.
- **The one weak question in every run** is "Wie kommt ein Kunde zu einem
  Konto, und wo landen seine Kartendaten?" (1/3, 0/3, 1/3): the answer has
  the user service and its endpoints in front of it and describes
  registration from the front-end's side. The other question that moved
  between runs, the services orders consults before saving (4/4 then 2/4 then
  asked back), is the judge and the module card taking turns, not the
  retrieval.
- **The first two runs met an unreliable upstream**: three stream errors
  and one empty answer in twenty turns, all on the Pro lane. The harness
  records them as failures and grades nothing; the product would show the
  reader a failed turn with a retry.

## The private estate, twenty questions

An nx workspace of three applications and four libraries, and a Maven parent
of four Spring Boot services and three libraries, indexed from local
checkouts and kept out of this repository with its questions and rubrics
(`eval/private/`, ignored). Twenty questions, sixteen German and four English,
each verified against the code; eighteen have every part inside one
repository, and two ask across the UI-to-service boundary that
`2026-09-11-declared-structure.md` says no token edge joins.

What reaches the answer (`TestFlowGathered`, same arms as the flow corpus):

| arm | parts | questions whole | mean sources |
|---|---|---|---|
| search only | 23/31 | 14/20 | 20.0 |
| symbol walk | 25/31 | 16/20 | 104.0 |
| symbol walk + crossings | 25/31 | 16/20 | 88.3 |
| **+ reranker (the product)** | **26/31** | **17/20** | 90.5 |

The three parts still out: the UI's route guard for an invalid link and the
bank-data refresh behind the UI's IBAN check — both the UI-to-service link
no literal joins, exactly as predicted — and the three `project.json`
manifests behind "which apps does the UI repository hold", which the answer
prompt gets from the units paragraph instead of from a chunk. The units
themselves came out as the build declares them: seven Maven modules with
their eight in-repository dependencies, nine nx projects with the three
e2e-to-app links and eleven import edges from apps to libraries.

The answers, judged:

| arm | run | rubric present | contradicted | forbidden asserted | parts cited | asked back | upstream failures |
|---|---|---|---|---|---|---|---|
| **reranked (the product)** | 1 | **50/65** | 2 | 0 | 23/31 | 0 | 0 |
| **reranked (the product)** | 2 | **44/59** | 0 | 0 | 21/28 | 1 | 1 |

Read across the two runs:

- **Rubric claims present sit at 75–77 %**, under the flow corpus's 84–96 %,
  on questions that each ask for three or four things about a real estate.
  The one failure is a read timeout on the gate lane while understanding the
  question; the one ask-back offers three candidates for what the close of a
  pre-registration returns, which the first run answered 2/3.
- **Two claims contradicted in the first run, none in the second, and no
  forbidden claim in forty grades.** The geocoding answer says two requests
  per claim are allowed: it read the number off a test that makes two calls,
  not the configured default of thirty, and got the rest (the total limit,
  the empty list with `TOO_MANY_REQUESTS`) right. The repository-parts answer
  named three of the seven Maven modules and said the extranet service was
  not indexed, with the units paragraph listing all seven in front of it; the
  second run named the parts without the contradiction. Both are the answer
  trusting one chunk over the paragraph.
- **Citation coverage is 23/31 and 21/28**, above the flow corpus's half.
  The question about the UI repository's apps is 3/3 present and 0/3 cited in
  both runs: the answer comes from the units paragraph, which is not a source,
  so there is nothing to cite. The address-suggestion question is 1/2 present
  with 0/1 cited both times; that is the UI-to-service crossing no literal
  joins, and the answer stays on the UI's side of it.
- **Four questions moved between runs** by two claims or more: the archive
  path (1/4 then 3/4), the pre-registration app's state between steps (3/4
  then 1/4), the supplementary submission (2/4 then 1/4) and BagOrd (3/3 then
  2/3). The retrieval is the same both times; the answer chooses what to
  explain, and the judge grades what it chose.
- A turn reads 65–110 sources and spends 24–37k tokens; a run of twenty is
  half a million.

## Reproducing

```
hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'                       # flow corpus, both runs
BACKEND_EVAL_RERANK=0 hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'  # the fused-order baseline

# a private estate: manifest, questions, expansions and rubrics under eval/private/
EVAL_DB=/tmp/rongo-smd.db EVAL_REPOS_FILE=private/smd-repos.yaml EVAL_REPO_ROOT=/tmp/rongo-smd-repos \
BACKEND_EVAL_FLOW_QUESTIONS=private/smd-questions.json BACKEND_EVAL_FLOW_EXPANSIONS=private/smd-expansions.json \
BACKEND_EVAL_RUBRICS=private/smd-rubrics.json hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'
```
