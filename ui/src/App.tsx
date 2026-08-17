import RepoList from "./RepoList";

export default function App() {
  return (
    <main className="mx-auto max-w-4xl p-8">
      <h1 className="text-2xl font-semibold tracking-tight">rongo</h1>
      <h2 className="mt-8 text-lg font-semibold tracking-tight">Repos</h2>
      <p className="mt-1 mb-4 text-sm text-[var(--color-ink-soft)]">
        Reine Anzeige. Die Repository-Liste wird in <code>repos.yaml</code> gepflegt, Zugangsdaten
        stehen nie darin.
      </p>
      <RepoList />
    </main>
  );
}
