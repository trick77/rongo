-- One forced re-index, for 0015's reason: .bpmn became a language of its own
-- (files.lang = 'bpmn', chunked by flow node, the diagram block left out) and
-- the answer prompt's process listing reads the models by that language. An
-- incremental poll only touches paths that changed since last_sha, so on an
-- existing deployment every model would keep its old lang, its coordinate
-- chunks and no listing until somebody edited it. Cheap: nothing unchanged is
-- re-embedded, and a model's new node chunks are a few hundred embeddings.
UPDATE repo_state SET last_sha = '';
