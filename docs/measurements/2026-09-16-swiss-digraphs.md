# Umlauts written as ae/oe/ue: the model, not the note

**Status: measured 2026-09-16. A German answer on gpt-5.4-mini came back
with "die Korrektheit der Rueckgabe". The Swiss note in the answer prompt
already forbids ae/oe/ue. Three wordings of that note were measured against
each other on gpt-5.4-mini and none moved the count; gpt-5.4 on the same
note is worse; MiMo Pro, the product's deployment, writes none at all. The
note stays as it was. What shipped is the number:
`TestEvalMeasureAnswers` now counts the digraphs in every German answer and
prints the words.**

## Why

The Swiss orthography note (`swissGerman`, `backend/internal/ask/answer.go`)
was added on 2026-09-05 and extended the same day to name the umlauts,
after a card offered "Sequenzdiagramm fuer Geschaeftsprozesse". Nothing in
the harness read an answer for its spelling: an answer with "Rueckgabe" in
it passes every rubric. So whether the note works was a belief, and the
belief was wrong for the deployed model.

## The counter

`germanDigraphs` (`backend/internal/retrieve/eval/digraphs_test.go`) drops
fenced and inline code and the citation markers, then lists every word of
the running text carrying ae, oe or ue. A "ue" after a, e or q is never an
umlaut (neue, Steuer, Vertrauen, Frequenz, Quelle), nor is the -uell suffix
(aktuell, manuell); a short list covers borrowings and names (Aerosol,
Koeffizient, Israel, true, value). `answerRecord.Digraphs` holds the words
for a German rubric, the per-question log line prints them, the run summary
sums them. Two false positives showed up in the runs below and are corrected
by hand in the table: "zuerst" (zu-erst, now on the list) and
"Kontoerstellung" (a compound seam, Konto-erstellung, left alone: the
printed word list is what catches that class, a longer list would not).

## The arms

`hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'` on the flow corpus
(`2026-09-10-flow-corpus.md`, ten questions, five of them German), two runs
each, judge on MiMo Pro as in every earlier table. The answer lane is named
by `BACKEND_EVAL_PRO_MODEL`; the gate lane is gpt-5.4-mini throughout, at
`off` for the gpt-5.4 arm as `2026-09-15-gpt-5-series.md` ran it.

Three wordings of the note on gpt-5.4-mini:

- **master**: the note as committed on 2026-09-05. English, names ä ö ü,
  forbids ae/oe/ue, quotes "fuer" and "Geschaeftsprozess" as the wrong forms.
- **source case**: English, adds that a word the sources spell with a digraph
  (a comment saying "Rueckgabe", an identifier `pruefeBetrag`) is written with
  its umlaut in prose, and only code keeps the source's spelling. This was the
  hypothesis: ASCII German sources licensing ASCII German prose.
- **German**: the note written in German, umlaut-spelled examples only, no
  digraph string anywhere in the prompt, so nothing primes the digraph tokens.

Digraphs are the corrected count over the five German answers of a run.

| answer lane | note | run | rubric present | contradicted | forbidden | parts cited | digraphs |
|---|---|---|---|---|---|---|---|
| gpt-5.4-mini | master | 1 | 25/30 | 0 | 0 | 21/30 | 2 |
| gpt-5.4-mini | master | 2 | 26/30 | 0 | 0 | 18/30 | 8 |
| gpt-5.4-mini | source case | 1 | 23/30 | 0 | 0 | 18/30 | 5 |
| gpt-5.4-mini | source case | 2 | 25/30 | 0 | 0 | 21/30 | 3 |
| gpt-5.4-mini | German | 1 | 27/30 | 0 | 0 | 17/30 | 7 |
| gpt-5.4-mini | German | 2 | 25/30 | 0 | 0 | 19/30 | 3 |
| gpt-5.4 | master | 1 | 25/27 | 0 | 0 | 20/28 | 23 |
| gpt-5.4 | master | 2 | 26/30 | 0 | 0 | 19/30 | 23 |
| **MiMo Pro (the product)** | master | 1 | 24/26 | 0 | 1 | 17/25 | **0** |
| **MiMo Pro (the product)** | master | 2 | 23/26 | 0 | 0 | 19/25 | **0** |

The words, gpt-5.4-mini, master: fuer, fuer; waehrend, eigenstaendige, fuer,
Kontenidentitaet, zusaetzlich, haelt, verknuepfte, enthaelt. Source case:
fuer, ueber, aufgeloest, zurueckgegeben; fuer; Ausloeser, ausgefuehrt; ueber.
German: Empfaengerseite, ueberwacht, ausgefuehrt, laesst, Geschaeftsprozess,
Empfaenger, ueber; Empfaenger, laesst, ausgefuehrte. gpt-5.4: abhoert,
uebergibt, Zahlungspruefung, laeuft, fuer, ueber, Vollstaendigkeitspruefung,
muessen, laedt, benoetigten, Datenbloecke, Abhaengigkeiten, naemlich,
sinngemaess, Betraege, ungueltig, Datensaetze, zurueckkommt, ausdruecklich,
and more of the same.

## Reading it

- **Three wordings, indistinguishable.** 2 and 8, 5 and 3, 7 and 3: the
  spread inside one wording is as wide as the spread between them. Rubric
  and citation columns move inside the judge's noise as well.
- **The digraphs are the model's own words, not the sources'.** Sock Shop is
  English code; there is no "Rueckgabe" to copy. The hits are function words
  and everyday verbs (fuer, ueber, laeuft, enthaelt), and they sit in the
  same sentence as correct umlauts: "die Karten-Referenz aufgeloest und nur
  die Kartennummer gekürzt zurueckgegeben". That is sampling, not a rule
  misread, and a rule does not fix sampling. The source-case hypothesis is
  not supported on this corpus; on a corpus with ASCII German sources it may
  add to the count, which the counter will show.
- **gpt-5.4 does it in nearly every sentence.** 23 per run against 2 to 8
  for the mini. `2026-09-15-gpt-5-series.md` named gpt-5.4 the best answer
  arm on the rubric; for a German reader this column says otherwise.
- **MiMo Pro writes none.** Zero in both runs, on answers that carry 7 to
  23 umlauts each; one question asked back per run, so five German answers
  are four. The product's deployment does not have the problem; the gpt-5
  series behind the gateway does.

## What remains

- A word-level replacement in the answer stream is not built. ue to ü is
  not reversible (neue, Feuer, Steuer, Bauer), and a gate over a class the
  model samples at random is the kind of hardening
  `rongo-diagram-fix-the-class` warns against.
- Untested: temperature on the answer lane; a corpus with ASCII German
  sources (the private estate).
- The number now exists. A model swap for German readers is judged by this
  column beside the rubric.
