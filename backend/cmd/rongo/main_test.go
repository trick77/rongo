package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/store"
)

// The hosts are llmwire's profiles' and the keys are llmwire's to read from
// the environment; what main owns is refusing to boot, with the variable
// named, when a key is missing.
func TestNewModelClients_namesTheMissingVariable(t *testing.T) {
	vars := []string{"LLMWIRE_OPENAI_API_KEY", "LLMWIRE_MIMO_API_KEY"}
	for _, v := range vars {
		t.Setenv(v, "x")
	}
	cfg := config.Config{LLMModel: "mimo-v2.6-flash", LLMGateModel: "mimo-v2.6-flash"}
	if _, _, err := newModelClients(cfg); err != nil {
		t.Fatalf("both keys set: %v", err)
	}
	for _, v := range vars {
		t.Run(v, func(t *testing.T) {
			t.Setenv(v, "")
			_, _, err := newModelClients(cfg)
			var me *llmwire.MissingEnvError
			if !errors.As(err, &me) || me.Var != v {
				t.Fatalf("got %v, want a MissingEnvError naming %s", err, v)
			}
		})
	}
}

// The vec0 table is created at embed.Model's width, and a file created at
// another width is refused rather than written into.
func TestMigrateForModel_refusesAFileBuiltForAnotherWidth(t *testing.T) {
	fresh, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := migrateForModel(fresh); err != nil {
		t.Fatalf("a fresh file: %v", err)
	}
	if built, _ := store.BuiltDim(fresh); built != embed.Dim() {
		t.Fatalf("built %d, want %d", built, embed.Dim())
	}

	other, err := store.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := store.Migrate(other, embed.Dim()+1); err != nil {
		t.Fatal(err)
	}
	err = migrateForModel(other)
	if err == nil || !strings.Contains(err.Error(), embed.Model) {
		t.Fatalf("got %v, want a refusal naming the model", err)
	}
}

// Both failures on the way to the width are reported as such, never as a
// width mismatch.
func TestMigrateForModel_namesTheStepThatFailed(t *testing.T) {
	closed, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := migrateForModel(closed); err == nil || !strings.Contains(err.Error(), "apply migrations") {
		t.Fatalf("closed database: got %v, want the migration step named", err)
	}

	gone, err := store.Open(filepath.Join(t.TempDir(), "gone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gone.Close()
	if err := migrateForModel(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := gone.Exec(`DROP TABLE chunks_vec`); err != nil {
		t.Fatal(err)
	}
	if err := migrateForModel(gone); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("vector table gone: got %v, want the read step named", err)
	}
}
