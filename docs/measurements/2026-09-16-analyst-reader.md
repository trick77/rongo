# The Analyst prompt names the reader: measured, no regression

**Status: measured 2026-09-16. `answerBA` gained a first paragraph that says
who the reader is and what a good answer gives them (the rule as a rule:
trigger, effect, duration, conditions; code, configuration or manual step
where the sources show it). The BA answer arm moved from 24/26 + 26/30 rubric
claims to 28/30 + 28/30, nothing contradicted, nothing forbidden, before and
after. Kept.**

## Why

A delay question for the PROD stage, asked as Analyst in German on the
gpt-5.4 deployment, came back correct and unreadable for its reader: the raw
cron in the opening sentence, field names and property keys in running text,
and never the figure the reader wanted (a 48-hour hold plus an hourly job, as
one range). Two causes in `backend/internal/ask/answer.go`:

- `answerBA` was bans only: no code, no signatures, no file paths. A model
  told what to avoid writes developer prose with the banned words removed.
  Nothing said who the reader is or what they want to hear.
- `answerStages`, appended after it, demands every value copied character for
  character. Right for anti-hallucination, and it put the literal in the lead.

Two changes, two PRs. #174 adds a values paragraph: say what a value means in
words, keep the literal once in parentheses after the words, out of the lead,
and state the combined outcome of interacting settings as one figure. This
one adds the role paragraph in front of it. The role paragraph restates
`answerShape`'s one-sentence lead (without it the list of wants lands in the
opening sentence whole) and conditions the code/config/manual classification on the
sources (`answerCommon` says invent nothing).

The umlaut mixing in the same answer (Verzögerung beside stuendlich) is
gpt-5.4 behaviour measured the same day, and the reason the Swiss spelling is
a string function rather than a prompt note; not a prompt change.

## The arms

`hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'` on the flow corpus
(`2026-09-10-flow-corpus.md`, ten questions, five German), two runs each,
answer and judge on MiMo Pro. Before is the #174 branch (values paragraph,
no role paragraph); after is this branch.

| arm | run | rubric present | contradicted | forbidden | cited parts | asked back |
|---|---|---|---|---|---|---|
| before | 1 | 24/26 | 0 | 0 | 15/25 | 1 |
| before | 2 | 26/30 | 0 | 0 | 17/30 | 0 |
| after | 1 | 28/30 | 0 | 0 | 18/30 | 0 |
| after | 2 | 28/30 | 0 | 0 | 18/30 | 0 |

The gain is inside judge noise (one or two questions per run re-roll); what
the table settles is that the register change costs no claim and no citation.

The corpus has no staged configuration, so the teaser-mail failure itself is
not reproducible here; the arm guards against regression, the register win is
checked on the deployment.

## What the role paragraph does to the text

Mentions of code-versus-configuration across the twenty answers rose from 13
to 33. The sampled ones are sourced: "hardcoded in the shipping service's
controller; there is no configuration for retries or alternative handling
shown in the sources [1][2]", "Die Höhe der Versandpauschale ist im
Systemcode fest hinterlegt [1]". One unmarked sentence followed such a
claim ("Sie ist nicht konfigurierbar") where the sources do show the
constant. That is the "where the sources do not show it, say nothing" clause
holding at the sentence, not at the paragraph; watched, not fixed.

Developer answers: one run at `BACKEND_EVAL_AUDIENCE=dev` on this branch,
rubric 21/25, 0 contradicted, 0 forbidden, cited 16/25, one question asked
back (the shipment-queue one, a routing decision), one failed with "the model
returned no answer text" after 638 completion tokens (the invoice question;
a delivery failure of the kind #163 retries once, and the retry delivered
nothing either). The dev prompt shares no text with `answerBA`, so neither is
this change; there is no earlier dev table on this corpus to set them
against.
