-- symbols holds definitions only, from here on.
--
-- It was written from every ctags record, while internal/ask reads it as
-- "where is this name DEFINED" and follows what it finds. The two disagreed,
-- and the walk resolved struct fields, locals, and every key and array index
-- of every JSON file: a question about routing reached `type ladder struct`
-- through its field `named`, and sixty-five array elements of an eval fixture
-- through the key `candidates`, while the functions that do the routing never
-- reached the prompt at all.
--
-- The writer now filters on indexer.definitionKinds. That alone would leave
-- the existing rows in place for as long as their file's sha does not change,
-- which for a JSON fixture or a schema file is indefinitely — so the rows go
-- here rather than waiting for a re-index.
--
-- The list is a copy of definitionKinds at the time of writing, and stays
-- frozen: a migration describes what it did to the database it ran against,
-- not what the Go set means today. Adding a kind later needs no migration
-- (nothing was deleted that a re-index will not restore); removing one does.
DELETE FROM symbols WHERE kind NOT IN (
    'func', 'function', 'method', 'procedure', 'subroutine', 'constructor',
    'class', 'struct', 'interface', 'trait', 'enum', 'type', 'typedef',
    'union', 'module', 'namespace', 'singletonMethod', 'macro',
    'table', 'alias'
);
