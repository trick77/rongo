package units

import (
	"context"
	"testing"
)

// TestScan_aRootPomIsTheRepositoryNotAPart: a single-module Maven build has
// one pom at the root. Its coordinates matter to repo_deps — that is how
// another repository's dependency on it is seen — but it is not a unit: a
// repository that is one build is not made of parts, and Sync must not store
// "." as one.
func TestScan_aRootPomIsTheRepositoryNotAPart(t *testing.T) {
	paths, read := fixtureRead(map[string]string{
		"pom.xml": `<project><groupId>works.weave.socks</groupId><artifactId>orders</artifactId>
<dependencies><dependency><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-web</artifactId></dependency></dependencies>
<build><plugins><plugin><artifactId>spring-boot-maven-plugin</artifactId></plugin></plugins></build></project>`,
	})
	us, deps, _ := Scan("orders", paths, read)
	if len(us) != 1 || us[0].Key != "." || us[0].Publishes != "works.weave.socks:orders" {
		t.Fatalf("units = %+v, want the root build with its coordinate", us)
	}
	if len(deps) != 1 || deps[0].Coordinate != "org.springframework.boot:spring-boot-starter-web" {
		t.Errorf("deps = %+v", deps)
	}
	if Of(us, "src/main/java/App.java") != nil {
		t.Errorf("a root build claimed a file as a part")
	}

	db := unitsDB(t)
	if err := Sync(context.Background(), db, "orders", us, deps); err != nil {
		t.Fatal(err)
	}
	got, gotDeps, err := Load(context.Background(), db, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(gotDeps) != 0 {
		t.Errorf("stored units = %+v deps = %+v, want none for a repository of one build", got, gotDeps)
	}
}
