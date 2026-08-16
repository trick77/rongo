# rongo

Codeverstehen für BA und DEV. Indexiert Repositories, klärt Mehrdeutiges zurück, antwortet belegt
in der gewählten Rolle. Produktname immer `rongo` — nie «Rongorongo», das ist nur Etymologie.

## Arbeitsweise
- Docs, Specs und Code-Kommentare: **Englisch**. Nutzertexte und UI-Copy: **Deutsch, Schweizer
  Rechtschreibung** — nie `ß`.
- Ein Feature-Branch je Phase (`feat/phase-N-...`), nie auf `master` committen.
- TDD: erst der fehlschlagende Test, dann die kleinste Implementierung.
- YAML immer `.yaml`, nie `.yml`. Container-Datei immer `Containerfile`.
- Kein Test ruft ein echtes LLM, einen echten Embedding-Endpunkt oder ein echtes Git-Remote —
  `httptest`-Attrappen, Fixture-Repo lokal mit `git` erzeugt.

## Festgelegte technische Entscheide (nicht ohne Absprache ändern)
- Modulpfad `github.com/trick77/rongo`. Go 1.26, stdlib `net/http`, kein Framework.
- **Pure-Go SQLite**: `ncruces/go-sqlite3` plus `asg017/sqlite-vec-go-bindings/ncruces` (wasm,
  ohne cgo) plus FTS5. `CGO_ENABLED=0` überall.
- Genau **eine** SQLite-Datei als Datenhaltung. Kein Postgres, kein Redis, kein Vektor-Dienst.
- Frontend React + TS + Vite + Tailwind, in das Binary eingebettet (`//go:embed`).
- Laufzeit-Image `debian:13-slim`, nicht distroless: rongo ruft `git`, `rg` und `ctags` auf.
- **Kein tree-sitter.** Bräuchte cgo und je Sprache Grammatik samt Knotennamen. `ctags` liefert
  für ~150 Sprachen einen uniformen Datensatz; wo nichts kommt, greift das Zeilenfenster.
- Konfiguration ausschliesslich über `RONGO_*`-Umgebungsvariablen.

## Modelle
- Zwei MiMo-Deployments, hart in `internal/llm/client.go`, **nie** als Umgebungsvariable.
- **Pro** nur, wo ein Mensch liest: die Antwort, die Fachkarten-Zusammenfassungen.
- **non-Pro + ShortGate** für alles andere: Verstehen, Routen, Relevanz beim Sammeln,
  Thread-Titel, Nachfrage-Prüfschritt. Massstab ist «Ausgabe ist ID/Label», nicht «denkt nicht».
- Beide sind Reasoning-Modelle. `WithoutThinking` und `ShortGate` sind getrennte Schalter.
- Jeder Aufruf bekommt `WithMaxTokens`, ausser eine abgeschnittene Antwort wäre schlimmer.
- Embeddings: OpenAI, `text-embedding-3-small`, 1536 Dimensionen.

## Invarianten (müssen in jedem Feature halten)
- **Nie erfinden.** Führt die Kette in nicht indexierten Code, wird das in der Antwort benannt —
  sichtbar ist der Aufruf und die Konfiguration, nicht das Innere. Eine plausible Erfindung ist
  der teuerste Fehler dieses Produkts.
- **Kein Treffer heisst kein Treffer.** «Nichts gefunden» samt versuchter Begriffe, nie eine
  Antwort aus dem, was zufällig im Kontext liegt.
- **Jede Aussage ist belegbar.** Belegfeld nennt Repo, Datei, Zeile. Zitierte Dateien werden beim
  Deckeln des Kontexts nie verdrängt — ein Beleg ins Leere ist schlimmer als eine lange Liste.
- **Der Thread ist ein Protokoll.** Eine Nachfrage erzeugt eine neue Antwort; die alte wird nie
  überschrieben. Auch eine korrigierte Rückfrage-Auswahl startet einen neuen Zug.
- **Rückfrage nur bei echter Mehrdeutigkeit.** Hängen Kandidaten laut `repo_deps` voneinander ab,
  ist es Zusammensetzung, keine Alternative — dann alle beantworten, nicht fragen.
- **Repo-Grenze nur mit zwei Gründen überschreiten**: der Code referenziert das Symbol wirklich,
  und das Zielrepo ist indexiert. Gleiches Sprungbudget, kein Rabatt.
- Zugangsdaten für Repositories werden verschlüsselt abgelegt, nie geloggt, nie von der API
  zurückgegeben.

## Oberfläche
- Aufklappbares trägt einen **Chevron**, der beim Öffnen um 90 Grad dreht. Kein Dreieck, kein
  Plus/Minus, kein Symbolwechsel.
- Aktivitätsspur als Timeline, **eine je Zug**. Eine Rückfrage beendet den Zug: Wartezustand-Knoten
  in Ochre, nicht der Done-Haken.
- Ochre heisst «du bist dran». Ist etwas entschieden, verliert es die Farbe.
- Kein Boustrophedon in der Oberfläche — bewusst verworfen, Lesbarkeit schlägt Motiv.
