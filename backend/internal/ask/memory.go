package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
)

// IntentMemory is the understanding's intent for a question that is only a
// standing instruction: "never show me flowcharts again", nothing asked.
const IntentMemory = "memory"

// remember applies the directive the understanding read, if any, through the
// caller's OnMemory — the pipeline has no reader and no store, the handler
// has both. The holder on the context is updated here, so the answer of the
// same turn is written under the rule just given. Returns what was stored, or
// nil when nothing was: memory off, no directive, no caller to write it.
//
// A full memory is the one refusal that is not an error of the turn: the
// question still runs, the trace says the rule was refused, and a turn that
// was only the rule says so in its answer.
func (p *Pipeline) remember(ctx context.Context, u Understanding, ev Events) (*memory.Added, error) {
	h := memory.From(ctx)
	d := u.Directive()
	if h == nil || d.Empty() || ev.OnMemory == nil {
		return nil, nil
	}
	ev.status("remembering")
	added, err := ev.OnMemory(d)
	if err != nil {
		if errors.Is(err, memory.ErrFull) {
			slog.Info("memory full", "thread", llm.ThreadID(ctx))
			ev.detail("remembering", map[string]any{"refused": "full"})
			return nil, err
		}
		return nil, fmt.Errorf("remember the instruction: %w", err)
	}
	h.Apply(added)
	slog.Info("remembered", "thread", llm.ThreadID(ctx), "memory", added.Row.Text,
		"scope", added.Row.Scope, "replaced", len(added.Replaced), "removed", len(added.Removed))
	ev.detail("remembering", rememberingDetail(added))
	return &added, nil
}

// rememberingDetail is what the step found: the rule as stored, what it
// replaced, what the reader forgot, and a scope the index did not carry.
func rememberingDetail(a memory.Added) map[string]any {
	d := map[string]any{}
	if a.Row.ID != 0 {
		d["memory"] = a.Row.Text
		if a.Row.Scope != "" {
			d["scope"] = a.Row.Scope
		}
	}
	if len(a.Replaced) > 0 {
		d["replaced"] = a.Replaced
	}
	if len(a.Removed) > 0 {
		d["removed"] = a.Removed
	}
	if a.ScopeDropped != "" {
		d["scope_dropped"] = a.ScopeDropped
	}
	return d
}

// memoryText is the fixed wording of a turn that was only an instruction,
// per language. Templated like nothingFound: no sources, so never a model
// call. The rule itself is quoted in English, the language it is kept in.
// German is written Swiss by hand.
type memoryText struct {
	noted, replaces, forgotten, scoped, scopeDropped, full, nothing string
}

var memoryTexts = map[Language]memoryText{
	LanguageEN: {
		noted:        "Noted: %q. It applies to every answer from now on and is listed on the Memory page.",
		replaces:     "It replaces: %s.",
		forgotten:    "Forgotten: %s.",
		scoped:       "It applies to %s.",
		scopeDropped: "No project or repository called %q is indexed, so it applies everywhere.",
		full:         "Memory is full: %d instructions are saved. Delete one on the Memory page to add this one.",
		nothing:      "No saved instruction matches. The Memory page lists what is kept.",
	},
	LanguageDE: {
		noted:        "Notiert: %q. Gilt ab jetzt für jede Antwort und steht auf der Seite Memory.",
		replaces:     "Ersetzt: %s.",
		forgotten:    "Vergessen: %s.",
		scoped:       "Gilt für %s.",
		scopeDropped: "Kein Projekt oder Repository namens %q ist indexiert, deshalb gilt es überall.",
		full:         "Das Memory ist voll: %d Anweisungen sind gespeichert. Lösche eine auf der Seite Memory, um diese hinzuzufügen.",
		nothing:      "Keine gespeicherte Anweisung passt dazu. Die Seite Memory zeigt, was gespeichert ist.",
	},
	LanguageFR: {
		noted:        "Noté : %q. S'applique désormais à chaque réponse et figure sur la page Memory.",
		replaces:     "Remplace : %s.",
		forgotten:    "Oublié : %s.",
		scoped:       "S'applique à %s.",
		scopeDropped: "Aucun projet ou dépôt nommé %q n'est indexé, donc cela s'applique partout.",
		full:         "La mémoire est pleine : %d instructions sont enregistrées. Supprimez-en une sur la page Memory pour ajouter celle-ci.",
		nothing:      "Aucune instruction enregistrée ne correspond. La page Memory liste ce qui est conservé.",
	},
	LanguageIT: {
		noted:        "Annotato: %q. Vale per ogni risposta da ora in poi ed è elencato nella pagina Memory.",
		replaces:     "Sostituisce: %s.",
		forgotten:    "Dimenticato: %s.",
		scoped:       "Vale per %s.",
		scopeDropped: "Nessun progetto o repository chiamato %q è indicizzato, quindi vale ovunque.",
		full:         "La memoria è piena: sono salvate %d istruzioni. Eliminane una nella pagina Memory per aggiungere questa.",
		nothing:      "Nessuna istruzione salvata corrisponde. La pagina Memory elenca ciò che è conservato.",
	},
}

// MemoryAnswer is the answer of a turn that was only an instruction: what
// was noted, what it replaced, what was forgotten. full is the refusal.
func MemoryAnswer(lang Language, a *memory.Added, full bool) string {
	t := memoryTexts[ParseLanguage(string(lang))]
	if full {
		return fmt.Sprintf(t.full, memory.MaxRows)
	}
	if a == nil {
		return t.nothing
	}
	var parts []string
	if a.Row.ID != 0 {
		parts = append(parts, fmt.Sprintf(t.noted, unstopped(a.Row.Text)))
		if a.Row.Scope != "" {
			parts = append(parts, fmt.Sprintf(t.scoped, a.Row.Scope))
		}
		if a.ScopeDropped != "" {
			parts = append(parts, fmt.Sprintf(t.scopeDropped, a.ScopeDropped))
		}
	}
	if len(a.Replaced) > 0 {
		parts = append(parts, fmt.Sprintf(t.replaces, quoted(a.Replaced)))
	}
	if len(a.Removed) > 0 {
		parts = append(parts, fmt.Sprintf(t.forgotten, quoted(a.Removed)))
	}
	if len(parts) == 0 {
		return t.nothing
	}
	return strings.Join(parts, " ")
}

func quoted(texts []string) string {
	q := make([]string, len(texts))
	for i, t := range texts {
		q[i] = fmt.Sprintf("%q", unstopped(t))
	}
	return strings.Join(q, ", ")
}

// unstopped drops a rule's own full stop before it is quoted inside a
// sentence that ends with one: "Noted: "Never draw flowcharts."." is two.
func unstopped(text string) string {
	return strings.TrimRight(strings.TrimSpace(text), ".")
}

// answerMemory ends a turn that was only an instruction: no search, no
// routing, no model call. The scope carries the intent so the record knows
// the turn for what it was — a follow-up skips it as an antecedent, a
// re-explain refuses it like any turn without sources.
func answerMemory(lang Language, added *memory.Added, err error) Answer {
	return Answer{
		Text:  MemoryAnswer(lang, added, errors.Is(err, memory.ErrFull)),
		Scope: Scope{Intent: IntentMemory},
	}
}
