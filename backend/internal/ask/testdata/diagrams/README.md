Answers as models actually wrote them, one file per shape that has been seen.

A diagram has been lost three times, and every time the fix was written
against the single deviation in front of it: too many actors, a src written
as prose, a fence the model tagged its own way. What was never tested was the
output the model produces, only the output the prompt asks for.

So this is the corpus, and it is the process: a diagram that fails to draw
gets its answer text dropped in here as a new file FIRST, and the fix
afterwards. Both ends read the same files - `renumber_corpus_test.go` on the
backend, `corpus.test.ts` in the UI - because an answer the backend passes
and the browser will not draw is the same defect as one neither touches.

A file is the raw answer text. Markers may name any source from 1 to 60; the
tests number that many. Every file must come out of the renumberer with its
one `mermaid` (or `diagram`) fence byte for byte as it came and the prose
around it renumbered, and the fence body must pass the renderer's own parser
(`mermaid.parse`). Drawing it is a browser's job and is checked by hand in
the running app.
