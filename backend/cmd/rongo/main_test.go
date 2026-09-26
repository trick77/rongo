package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/llm/llmtest"
	"github.com/trick77/rongo/internal/store"
)

// The hosts are llmwire's profiles' and the keys are llmwire's to read from
// the environment; what main owns is refusing to boot, with the variable
// named, when a key is missing. The chat lanes run on synthetic models; the
// embedder is a constant of the build and reads the real environment.
func TestNewModelClients_namesTheMissingVariable(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	cfg := config.Config{LLMModel: llmtest.Answer, LLMGateModel: llmtest.Answer}
	chat := llm.Config{Registry: llmtest.Registry(), Lookup: srv.Lookup}
	ep, err := llmwire.Default().LookupEmbedding(embed.Model)
	if err != nil {
		t.Fatal(err)
	}
	embedKey := ep.APIKeyEnv()

	t.Setenv(embedKey, "x")
	if _, _, err := newModelClients(cfg, chat); err != nil {
		t.Fatalf("both keys set: %v", err)
	}

	t.Run(embedKey, func(t *testing.T) {
		t.Setenv(embedKey, "")
		_, _, err := newModelClients(cfg, chat)
		var me *llmwire.MissingEnvError
		if !errors.As(err, &me) || me.Var != embedKey {
			t.Fatalf("got %v, want a MissingEnvError naming %s", err, embedKey)
		}
	})

	t.Run("chat key", func(t *testing.T) {
		const key = "LLMWIRE_LLMWIRETEST_API_KEY"
		noKey := chat
		noKey.Lookup = func(name string) (string, bool) {
			if name == key {
				return "", false
			}
			return srv.Lookup(name)
		}
		_, _, err := newModelClients(cfg, noKey)
		var me *llmwire.MissingEnvError
		if !errors.As(err, &me) || me.Var != key {
			t.Fatalf("got %v, want a MissingEnvError naming %s", err, key)
		}
	})
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
