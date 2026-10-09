/** A path as the viewers print it: the directory with its trailing slash in
 * the dim ink, the file name in the bright. */
export function splitPath(path: string): { dir: string; base: string } {
  const slash = path.lastIndexOf("/");
  return { dir: slash >= 0 ? path.slice(0, slash + 1) : "", base: path.slice(slash + 1) };
}

/** The first seven characters of a commit, the way git prints one. */
export function shortSha(sha: string | undefined): string {
  return (sha ?? "").slice(0, 7);
}

/** The day of an RFC 3339 stamp, as YYYY-MM-DD, or "" when unknown. */
export function isoDay(iso: string | undefined): string {
  return (iso ?? "").slice(0, 10);
}
