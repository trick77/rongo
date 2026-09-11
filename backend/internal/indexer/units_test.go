package indexer

import (
	"context"
	"strings"
	"testing"
)

func TestIndexRepoRecordsTheUnitsAnNxWorkspaceDeclares(t *testing.T) {
	// Given: an nx workspace with an app that imports a library through a
	// tsconfig alias, and a dependency's own project.json that must not count.
	h := newHarnessFiles(t, map[string]string{
		"nx.json":                     `{}`,
		"tsconfig.base.json":          `{"compilerOptions":{"paths":{"@shared":["libs/shared/src/index.ts"]}}}`,
		"apps/claims/project.json":    `{"name":"claims","projectType":"application","tags":["type:app"]}`,
		"apps/claims/src/main.ts":     "import { thing } from '@shared';\nconsole.log(thing);\n",
		"libs/shared/project.json":    `{"name":"shared","projectType":"library"}`,
		"libs/shared/src/index.ts":    "export const thing = 1;\n",
		"node_modules/x/project.json": `{"name":"not-ours"}`,
		"third_party/y/project.json":  `{"name":"vendored"}`,
		"out/apps/z/project.json":     `{"name":"built"}`,
	}, nil)
	st := h.stateOf(t)

	// When
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Then: two units, and the import became a dependency.
	rows, err := h.db.Query(`SELECT key, kind, name FROM units WHERE repo = ? ORDER BY key`, h.spec.Name)
	if err != nil {
		t.Fatalf("read units: %v", err)
	}
	var got []string
	for rows.Next() {
		var k, kind, name string
		if err := rows.Scan(&k, &kind, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, k+"="+kind+":"+name)
	}
	rows.Close()
	if want := "apps/claims=nx-app:claims,libs/shared=nx-lib:shared"; strings.Join(got, ",") != want {
		t.Errorf("units = %v, want %s", got, want)
	}
	var from, to string
	if err := h.db.QueryRow(`SELECT from_key, to_key FROM unit_deps WHERE repo = ?`, h.spec.Name).Scan(&from, &to); err != nil {
		t.Fatalf("read unit_deps: %v", err)
	}
	if from != "apps/claims" || to != "libs/shared" {
		t.Errorf("unit_deps = %s -> %s, want the app using the library it imports", from, to)
	}
}

func TestIndexRepoRecordsMavenCoordinatesAsRepositoryDependencies(t *testing.T) {
	// Given: a Maven module publishing one artifact and pulling another from
	// outside the repository. repo_deps is what routing joins across
	// repositories, and a Java estate declares its edges in poms, not go.mod.
	h := newHarnessFiles(t, map[string]string{
		"pom.xml": `<project><groupId>ch.example</groupId><artifactId>parent</artifactId><packaging>pom</packaging><modules><module>svc</module></modules></project>`,
		"svc/pom.xml": `<project><parent><groupId>ch.example</groupId><artifactId>parent</artifactId></parent><artifactId>svc</artifactId>
<dependencies><dependency><groupId>ch.example.other</groupId><artifactId>contract</artifactId></dependency></dependencies></project>`,
		"svc/src/main/java/App.java": "class App {}\n",
	}, nil)
	st := h.stateOf(t)

	// When
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), nil); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Then
	var publishes, requires string
	if err := h.db.QueryRow(`SELECT coordinate FROM repo_deps WHERE repo = ? AND direction = 'publishes'`, h.spec.Name).Scan(&publishes); err != nil {
		t.Fatalf("read published coordinate: %v", err)
	}
	if err := h.db.QueryRow(`SELECT coordinate FROM repo_deps WHERE repo = ? AND direction = 'requires'`, h.spec.Name).Scan(&requires); err != nil {
		t.Fatalf("read required coordinate: %v", err)
	}
	if publishes != "ch.example:svc" || requires != "ch.example.other:contract" {
		t.Errorf("repo_deps publishes %q requires %q", publishes, requires)
	}
}
