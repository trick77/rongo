# Deployed configuration: a property edge, declared stages, and redaction

**Status: built and measured 2026-09-13. On a private estate with an
infrastructure repository added, "how often is the digest mail sent" answers
with the code default and every stage's value, each cited from its own
file, and "… in production" with the prod value alone. On the pinned flow
corpus the product arm holds at 27-28/30; the non-rerank arm moves between
25 and 27 with property tokens AND without them, which is re-index order,
not the edge.**

## What was built

Three things, in the order the constraints run:

1. **Redaction** (`internal/redact`). A configuration file's credential
   values are replaced by `<redacted>` before selection, chunking, FTS, the
   embedding and edge extraction, and `sourceview.Read` applies the same
   function to what it reads from git. The rule is a regex over the key
   name and the value shape, never a prompt. Secret and SealedSecret
   manifests are skipped whole. Migration `0020` forces one full re-index
   so rows written before the rule are rewritten.
2. **A property edge** (`edges.KindProperty`). `${a.b}` in a JVM string
   literal and `a.b=` in a `.properties` file are the same token; the
   crossing lands on the key line of every file setting it, in the
   infrastructure repository and — for a property read by code — in the
   repository's own defaults file. Property landings run in a second pass
   after every route and destination landing and take no far-side symbol
   hop.
3. **Declared stages** (`stages:` in repos.yaml, `repo_stages`). A stage
   name or alias in the question narrows the repositories declaring stages
   to that directory, in both lanes and the crossing; a source under a
   stage directory is labelled `(stage X)` and the answer prompt says what
   that is.

## The private estate

Three repositories of one product plus its infrastructure repository as a
`snapshot: true` entry with `prod` (aliases production, produktion,
produktiv) and `intg` declared; a third stage directory left undeclared.
Local checkouts, the answer read through the product's own API.

| question | stage resolved | crossings | answer |
|---|---|---|---|
| how often is the digest mail sent automatically | none | 8 (one property key to the three stage files and the defaults file) | default `0/20 * * ? * * *` from the service's defaults file, then intg, syst, prod each from its own `application.properties`, each cited |
| … in production | `prod` (reader's word) | 7 | the prod value, the default named as overridden, "no other stages were looked at"; only `prod/` cited from the infrastructure repository |
| the same in German ("in der Produktion") | `prod` (alias) | 7 | same shape, in German |

The first build of the crossing answered the first question from ONE stage
file: the landing on intg took its far-side symbol hop, the words in a
properties file joined whatever was called `processor` or `cleanup`, and
the reserve was gone before prod. A property landing now takes no symbol
hop; a stage costs one chunk, and all of them fit.

The second build cited every stage but misread the default. The stage rule
now says every value is copied character for character from the line that
sets it; the third and fourth runs quote it as written.

What the viewer serves for the cited prod file: ten lines `<redacted>`,
the `${ENV}` placeholders and the cron lines intact, no `ENC(` anywhere. In
the index: 41 redacted chunks in the infrastructure repository, six sealed
secrets skipped whole, zero chunks holding `ENC(` other than a shell
script's `ENC($var)` (a source file, not a configuration file, and a
variable).

## The pinned flow corpus

`TestFlowGathered`, flow corpus re-indexed with property tokens (46 of
them, 14 shared across repositories, none crossing more than four):

| arm | with property tokens (three re-indexes) | without (three re-indexes) | documented 2026-09-11 |
|---|---|---|---|
| symbol walk | 20/30 | 20/30 | 20/30 |
| symbol walk + crossings | 25, 26, 25 /30 | 27, 25, 25 /30 | 26, 27 /30 |
| **+ reranker (the product)** | **28, 27, 27 /30** | 27, 27, 28 /30 | 28/30 |

The crossing arm moves between 25 and 27 whether property tokens exist or
not: the walk's budget cut falls on a different chunk after every re-index
(chunk ids, and so tie order, are assigned in index order), and one
question's landing then arrives or does not. With the trace on, the
property pass never ran on this corpus at all — the reserve was spent by
routes and destinations before it — so the tokens could not have moved
the number. Read a one-part move on this arm as noise, and re-index twice
before reading anything else.

`TestEvalMeasureAnswers` was not run for this change: the sources in front
of the model on the flow corpus are the same with and without the edge,
and the answer arm would have measured the judge.

## What this does not settle

- `application*.yaml` as a property source: only `.properties` yields
  keys. Arbitrary yaml never will — every manifest would emit
  `spec.template.spec.containers`.
- `@ConfigurationProperties(prefix)` composition: a class binding a prefix
  reads keys no `${…}` names.
- The reserve: property landings are one chunk each and run last, but an
  infrastructure repository with ten stages costs ten chunks of the
  4000-token reserve per key.
