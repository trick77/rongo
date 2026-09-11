package units

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

func fixtureRead(files map[string]string) (paths []string, read Read) {
	for p := range files {
		paths = append(paths, p)
	}
	return paths, func(p string) ([]byte, error) { return []byte(files[p]), nil }
}

func keys(us []Unit) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Key
	}
	return out
}

func TestScan_readsAnNxWorkspace(t *testing.T) {
	paths, read := fixtureRead(map[string]string{
		"nx.json":                          `{}`,
		"apps/claims/project.json":         `{"name":"claims","projectType":"application","sourceRoot":"apps/claims/src","tags":["domain:claims","type:app"]}`,
		"apps/claims-e2e/project.json":     `{"name":"claims-e2e","projectType":"application","implicitDependencies":["claims"],"tags":["type:e2e"]}`,
		"libs/shared/project.json":         `{"name":"shared","projectType":"library"}`,
		"node_modules/x/project.json":      `{"name":"somebody-elses"}`,
		"apps/claims/src/main.ts":          `import {} from "@shared";`,
		"libs/shared/src/index.ts":         ``,
		"tsconfig.base.json":               `{ "compilerOptions": { /* aliases */ "paths": { "@shared": ["libs/shared/src/index.ts"], "@claims/*": ["apps/claims/src/*"] } } }`,
		"libs/shared/README.md":            `not a manifest`,
		"apps/claims/src/app/project.json": "",
	})
	us, deps, skipped := Scan("ui", paths, read)
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "apps/claims/src/app/project.json") {
		t.Errorf("skipped = %v, want the empty project.json and nothing else", skipped)
	}
	if got, want := strings.Join(keys(us), ","), "apps/claims,apps/claims-e2e,libs/shared"; got != want {
		t.Fatalf("units = %v, want %v", got, want)
	}
	if us[0].Kind != KindNxApp || us[0].Name != "claims" || strings.Join(us[0].Tags, " ") != "domain:claims type:app" {
		t.Errorf("claims = %+v", us[0])
	}
	if us[2].Kind != KindNxLib {
		t.Errorf("shared kind = %s, want a library", us[2].Kind)
	}
	if len(deps) != 1 || deps[0].From != "apps/claims-e2e" || deps[0].To != "apps/claims" {
		t.Errorf("deps = %+v, want the implicit dependency resolved to the app's directory", deps)
	}
	aliases := Aliases(paths, read)
	if aliases["@shared"] != "libs/shared/src/index.ts" || aliases["@claims"] != "apps/claims/src" {
		t.Errorf("aliases = %v", aliases)
	}
}

func TestScan_readsAMavenMultiModuleBuild(t *testing.T) {
	parent := `<project><groupId>ch.example.claims</groupId><artifactId>claims-parent</artifactId><packaging>pom</packaging>
<modules><module>lib/persistence</module><module>service/intranet</module></modules></project>`
	persistence := `<project><parent><groupId>ch.example.claims</groupId><artifactId>claims-parent</artifactId></parent>
<artifactId>claims-persistence</artifactId>
<dependencyManagement><dependencies><dependency><groupId>ch.example.managed</groupId><artifactId>pinned-only</artifactId></dependency></dependencies></dependencyManagement>
<dependencies><dependency><groupId>org.springframework</groupId><artifactId>spring-jdbc</artifactId></dependency></dependencies>
<build><plugins><plugin><artifactId>some-plugin</artifactId><dependencies><dependency><groupId>ch.example.plugin</groupId><artifactId>helper</artifactId></dependency></dependencies></plugin></plugins></build>
<profiles><profile><id>it</id><dependencies><dependency><groupId>org.testcontainers</groupId><artifactId>postgresql</artifactId></dependency></dependencies></profile></profiles>
<reporting><plugins><plugin><artifactId>rep</artifactId><dependencies><dependency><groupId>org.rep</groupId><artifactId>repdep</artifactId></dependency></dependencies></plugin></plugins></reporting></project>`
	intranet := `<project><parent><groupId>ch.example.claims</groupId><artifactId>claims-parent</artifactId></parent>
<artifactId>claims-intranet-service</artifactId>
<dependencies>
  <dependency><groupId>${project.groupId}</groupId><artifactId>claims-persistence</artifactId></dependency>
  <dependency><groupId>ch.example.workflow</groupId><artifactId>camunda-intranet</artifactId></dependency>
</dependencies>
<build><plugins><plugin><groupId>org.springframework.boot</groupId><artifactId>spring-boot-maven-plugin</artifactId></plugin></plugins></build></project>`
	paths, read := fixtureRead(map[string]string{
		"pom.xml": parent, "lib/persistence/pom.xml": persistence, "service/intranet/pom.xml": intranet,
		"target/classes/pom.xml": parent,
	})
	us, deps, skipped := Scan("svc", paths, read)
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
	if got, want := strings.Join(keys(us), ","), "lib/persistence,service/intranet"; got != want {
		t.Fatalf("units = %v, want %v (the parent is a build of builds, not a unit)", got, want)
	}
	if us[0].Kind != KindMavenLibrary || us[0].Name != "claims-persistence" || us[0].Publishes != "ch.example.claims:claims-persistence" {
		t.Errorf("persistence = %+v", us[0])
	}
	if us[1].Kind != KindMavenService {
		t.Errorf("intranet kind = %s, want a service: it carries the boot plugin", us[1].Kind)
	}
	var internal, external []string
	for _, d := range deps {
		if d.To != "" {
			internal = append(internal, d.From+"->"+d.To)
		} else {
			external = append(external, d.From+"->"+d.Coordinate)
		}
	}
	if strings.Join(internal, ",") != "service/intranet->lib/persistence" {
		t.Errorf("internal deps = %v", internal)
	}
	if strings.Join(external, ",") != "lib/persistence->org.springframework:spring-jdbc,service/intranet->ch.example.workflow:camunda-intranet" {
		t.Errorf("external deps = %v (a managed version, a plugin's, a profile's or a report's dependency is not an edge)", external)
	}
}

func TestScan_readsAGradleMultiProjectBuild(t *testing.T) {
	paths, read := fixtureRead(map[string]string{
		"settings.gradle.kts": `rootProject.name = "shop"
include(":core", ":api")
include(":tools:migrate")`,
		"core/build.gradle.kts": `group = "ch.example.shop"
dependencies { implementation("org.slf4j:slf4j-api:2.0.0") }`,
		"api/build.gradle.kts": `plugins { id("org.springframework.boot") version "3.2.0" }
dependencies {
  implementation(project(":core"))
  implementation("com.fasterxml.jackson.core:jackson-databind:2.17.0")
}`,
	})
	us, deps, _ := Scan("shop", paths, read)
	if got, want := strings.Join(keys(us), ","), "api,core,tools/migrate"; got != want {
		t.Fatalf("units = %v, want %v", got, want)
	}
	if us[0].Kind != KindGradleService || us[1].Kind != KindGradleLibrary || us[1].Publishes != "ch.example.shop:core" {
		t.Errorf("api = %+v, core = %+v", us[0], us[1])
	}
	var got []string
	for _, d := range deps {
		got = append(got, d.From+"->"+d.To+d.Coordinate)
	}
	// Sorted by from, then to, then coordinate: an external coordinate has an
	// empty to and sorts before a sibling.
	want := "api->com.fasterxml.jackson.core:jackson-databind,api->core,core->org.slf4j:slf4j-api"
	if strings.Join(got, ",") != want {
		t.Errorf("deps = %v, want %s", got, want)
	}
}

func TestScan_aGoModuleInASubdirectoryIsAUnitAndTheRootIsNot(t *testing.T) {
	paths, read := fixtureRead(map[string]string{
		"go.mod":         "module github.com/example/root\n\ngo 1.22\n",
		"backend/go.mod": "module github.com/example/root/backend\n\ngo 1.22\n",
	})
	us, _, _ := Scan("r", paths, read)
	if len(us) != 1 || us[0].Key != "backend" || us[0].Name != "backend" || us[0].Kind != KindGoModule {
		t.Errorf("units = %+v, want the backend module alone", us)
	}
}

func TestOf_picksTheLongestPrefix(t *testing.T) {
	us := []Unit{{Key: "apps"}, {Key: "apps/claims"}, {Key: "libs/shared"}}
	if u := Of(us, "apps/claims/src/main.ts"); u == nil || u.Key != "apps/claims" {
		t.Errorf("Of = %v", u)
	}
	if u := Of(us, "apps-other/x.ts"); u != nil {
		t.Errorf("a sibling directory sharing a prefix string matched: %v", u)
	}
	if u := Of(us, "README.md"); u != nil {
		t.Errorf("a root file matched %v", u)
	}
}

func unitsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSync_replacesAndLoadsAndLinks(t *testing.T) {
	db := unitsDB(t)
	ctx := context.Background()
	us := []Unit{{Key: "apps/claims", Kind: KindNxApp, Name: "claims", Tags: []string{"type:app"}}, {Key: "libs/shared", Kind: KindNxLib, Name: "shared"}}
	if err := Sync(ctx, db, "ui", us, []Dep{{From: "apps/claims", To: "libs/shared"}, {From: "apps/claims", Coordinate: "@angular/core"}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Replaced whole: the second sync drops the library.
	if err := Sync(ctx, db, "ui", us[:1], nil); err != nil {
		t.Fatalf("Sync again: %v", err)
	}
	got, deps, err := Load(ctx, db, "ui")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "claims" || got[0].Repo != "ui" || strings.Join(got[0].Tags, " ") != "type:app" || len(deps) != 0 {
		t.Errorf("after replace: units = %+v deps = %+v", got, deps)
	}
	if err := Sync(ctx, db, "ui", us, []Dep{{From: "apps/claims", To: "libs/shared"}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	linked, err := Linked(ctx, db, "ui", "libs/shared", "apps/claims")
	if err != nil || !linked {
		t.Errorf("Linked = %v, %v; want true in either direction", linked, err)
	}
	linked, _ = Linked(ctx, db, "ui", "apps/claims", "apps/other")
	if linked {
		t.Errorf("unrelated units read as linked")
	}
}

func TestImportDeps_readsAliasImportsOutOfTheIndex(t *testing.T) {
	db := unitsDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES ('ui', 'file:///x', 'main')`); err != nil {
		t.Fatal(err)
	}
	seed := func(p, text string) {
		res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('ui', ?, 'sha')`, p)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO chunks (file_id, ordinal, start_line, end_line, text, raw_text, content_hash) VALUES (?, 0, 1, 5, ?, ?, ?)`, id, text, text, p); err != nil {
			t.Fatal(err)
		}
	}
	seed("apps/claims/src/app/app.component.ts", `import { Thing } from '@shared';
import { Other } from "@shared/sub";
import { Local } from './local';`)
	seed("libs/shared/src/index.ts", `export const Thing = 1; import x from "@angular/core";`)
	seed("apps/claims/README.md", `from "@shared" in prose is not an import`)
	us := []Unit{{Key: "apps/claims", Kind: KindNxApp, Name: "claims"}, {Key: "libs/shared", Kind: KindNxLib, Name: "shared"}}
	imports, err := ImportDeps(ctx, db, "ui", us, map[string]string{"@shared": "libs/shared/src/index.ts"})
	if err != nil {
		t.Fatalf("ImportDeps: %v", err)
	}
	if err := Sync(ctx, db, "ui", us, imports); err != nil {
		t.Fatal(err)
	}
	_, deps, err := Load(ctx, db, "ui")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].From != "apps/claims" || deps[0].To != "libs/shared" {
		t.Errorf("deps = %+v, want claims -> shared once", deps)
	}
}

func TestDescribe_rendersPartsAndConnectionsAndNothingForAPlainRepository(t *testing.T) {
	if got := Describe("rongo", nil, nil); got != "" {
		t.Errorf("a repository without units described itself: %q", got)
	}
	us := []Unit{
		{Key: "lib/persistence", Kind: KindMavenLibrary, Name: "claims-persistence"},
		{Key: "service/intranet", Kind: KindMavenService, Name: "claims-intranet-service"},
	}
	deps := []Dep{
		{From: "service/intranet", To: "lib/persistence"},
		{From: "service/intranet", Coordinate: "ch.example.workflow:camunda-intranet"},
	}
	got := Describe("svc", us, deps)
	for _, want := range []string{
		`Repository "svc" is built from 2 parts:`,
		"claims-persistence (library, lib/persistence)",
		"claims-intranet-service (service, service/intranet)",
		"claims-intranet-service uses claims-persistence.",
		"claims-intranet-service also depends on ch.example.workflow:camunda-intranet, which are outside this repository.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
