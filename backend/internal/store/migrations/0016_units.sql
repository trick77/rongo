-- units: the pieces a repository is BUILT from, as its own build tool declares
-- them — an nx application or library, a Maven or Gradle module, a Go module in
-- a subdirectory. They are what a person who knows the product calls the code
-- ("the Vorerfassung app", "the intranet service"), where the directory cut in
-- internal/modules can only offer apps/vorerfassung/src/app.
--
-- Read from manifests, never from documentation and never from a model:
-- project.json, tsconfig.base.json, pom.xml, settings.gradle, go.mod. Replaced
-- whole on every index run of the repository, so a unit cannot outlive the
-- manifest that declared it.
CREATE TABLE units (
    repo TEXT NOT NULL,
    -- key is the unit's directory, repo-relative, and what a file's path is
    -- matched against by longest prefix. "." is never a unit: a repository
    -- that is one build is not made of parts.
    key  TEXT NOT NULL,
    -- kind says which manifest declared it and what it is: nx-app, nx-lib,
    -- maven-service, maven-library, gradle-service, gradle-library, go-module.
    kind TEXT NOT NULL,
    -- name is what the manifest calls it: the nx project name, the Maven
    -- artifactId, the Gradle project path, the Go module's last element.
    name TEXT NOT NULL,
    -- tags are the nx tags, joined by a space; empty elsewhere.
    tags TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, key)
);

-- unit_deps: which unit uses which, inside one repository. to_key names a
-- sibling unit; an empty to_key with a coordinate is a dependency on something
-- outside the repository (a Maven artifact, an npm package), kept so the
-- answer prompt can say a service pulls a library the index does not carry.
CREATE TABLE unit_deps (
    repo       TEXT NOT NULL,
    from_key   TEXT NOT NULL,
    to_key     TEXT NOT NULL DEFAULT '',
    coordinate TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, from_key, to_key, coordinate)
);

-- One forced re-index, for 0015's reason: units are written by the index
-- run and an incremental poll would leave the table empty for as long as a
-- settled repository sits untouched. The same run re-derives integration
-- tokens under the route rules that arrived with units (a class-level
-- @RequestMapping prefix composed with the method path, a route read out of
-- a generated client's template literal), which an unchanged file would
-- otherwise never get. Cheap: nothing unchanged is re-embedded.
UPDATE repo_state SET last_sha = '';
