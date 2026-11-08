package store

import (
	"path/filepath"
	"testing"

	"github.com/trick77/ship/kit/schemadoc"

	"github.com/trick77/rongo/internal/embed"
)

// TestSchemaDoc keeps docs/schema.md in step with the migrations and the
// model.go row types. openTemp goes through Open, not a bare driver: the vec0
// table needs the sqlite-vec build it links. embed.Dim, not a test width, so
// the doc shows the vec0 table production builds.
func TestSchemaDoc(t *testing.T) {
	db := openTemp(t)
	if err := Migrate(db, embed.Dim()); err != nil {
		t.Fatal(err)
	}
	s, err := schemadoc.Describe(t.Context(), db, schemadoc.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	es, err := schemadoc.Entities(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schemadoc.Render(s, es)
	if err != nil {
		t.Fatal(err)
	}
	schemadoc.Golden(t, doc, filepath.Join("..", "..", "..", "docs", "schema.md"))
}
