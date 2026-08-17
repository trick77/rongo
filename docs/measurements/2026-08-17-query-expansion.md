# What query expansion buys

The phase-3 measurement found that the search runs on the raw question, though
the design never proposed that: step 1 of the question pipeline produces
*«erweiterte Suchbegriffe samt geratenem Codevokabular»*, and the spec's
«Anfrageseite» row sends the query in twice — once in business language, once in
code vocabulary. Six of 28 questions missed, and the gap looked like vocabulary:
the question says "Apple TV", the code says "AirPlay".

Phase 4a builds that step. This measures it.

## Method

Same corpus, same index (`text-embedding-3-small`, 1536), same 28 questions as
the phase-2 and phase-3 measurements. One arm searches the raw question; the
other searches the question **plus** the two expansions the understanding step
produced — the business-language restatement and the guessed identifiers — each
as its own semantic lane before fusion.

The expansions are generated once by `TestExpandQuestions` (one short-gate call
per question) and frozen in `expansions.json`. The model is not deterministic;
freezing its output is what makes this a measurement of retrieval rather than of
the model's mood on the day.

The criterion was fixed before the run: **the three questions sitting at ranks
23–25 must reach the top 5, and no question that already worked may fall out.**

## Result

| Arm | recall@5 | recall@20 | MRR |
|---|---|---|---|
| raw question | 0.679 | 0.786 | 0.476 |
| **question + expansion** | **0.679** | **0.893** | **0.546** |

**The criterion is half met, and the halves point in different directions.**

Three of the six failures are now found, two of them in the top 5:

| Question | raw | expanded |
|---|---|---|
| Was passiert bei vielen Extractor-Fehlern? | not found | **6** |
| Welche Wörter werden aus einer Frage entfernt? | not found | **2** |
| Wohin schreibt loom eine hochgeladene Datei? | not found | **4** |

And the ranking improves broadly: `ResolveOutputPath` 3 → 1, `Dateiname
bereinigt` 4 → 1, `Thread-Titel bereinigt` 14 → 6, `API-Token` 13 → 10. MRR
gains 15 % relative, which is the largest single move any change has produced on
this question set.

But three questions that worked got worse, and that is why recall@5 does not
move at all:

| Question | raw | expanded |
|---|---|---|
| Kanalfilter vor der Nachbarschaftssuche | 5 | 12 |
| Wie viele Volltext-Treffer liefert die Nachrichtensuche? | 2 | 7 |
| Was passiert, wenn eine Benutzer-Direktive zu lang wird? | 2 | 5 |

So: **expansion finds more and ranks the whole field better, but it dilutes the
top of the list.** Three lanes vote, and two of them are guesses; when the guess
is good it pulls the right file up from nowhere, and when it is not it pulls a
plausible neighbour up instead.

Three of the six remain missing: `download/freebytes.go`,
`playbackgrant/store.go` and `chat/share_store.go`.

## Where the guesses go wrong, in the model's own words

The expansions are in `expansions.json` and worth reading. For "viele Videos mit
Extractor-Fehlern" the model guessed `MediaExtractor`, `MediaCodec`, `ExoPlayer`
— Android media vocabulary that appears nowhere in peeq, which is a Go service
shelling out to yt-dlp. The question was still found, through the
business-language lane rather than the code lane.

That is the shape of the failure mode: the model guesses the vocabulary of the
technology it assumes, not of the code that exists. It cannot know, and a wrong
guess costs a lane.

## What this does not settle

`playbackgrant/store.go` still misses at K=20 — and yet, driving the real
pipeline through the UI, the answer to "Wie kommt ein Apple TV ohne Anmeldung an
die Mediendatei?" cites exactly that file. The gathering step reaches it by
following symbol references out of the handler, which the search alone never
returns.

So retrieval recall is the wrong lens for judging the product: what the answer
is written from is search **plus** the reference walk, and this harness measures
only the first half. A measurement of what actually reaches the answer needs the
gatherer in the loop, and that is worth building before tuning anything here.

## When to revisit

Together with the standing recommendation from phase 2: the question set needs
80–100 entries. At 28, "three gained, three lost" is a wash that a slightly
different set would report as a clear win or a clear loss.
