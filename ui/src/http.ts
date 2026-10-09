/** What a failed response says to the reader: the server's own message when
 * it wrote one (not in the checkout, too long, binary), else the status. A
 * bare status is not a message. */
export async function serverMessage(res: Response): Promise<string> {
  return (await res.text()).trim() || `The server answered with ${res.status}.`;
}

/** A server that answered and said no, with what it said. Told apart from a
 * connection that failed, which has nothing to quote. */
export class Refused extends Error {}

/** Reads one JSON resource. A refusal is thrown as Refused carrying the
 * server's message; the body comes back as res.json() hands it over, the
 * server's shape and not a typed one. */
export async function fetchJSON(url: string): Promise<any> {
  const res = await fetch(url);
  if (!res.ok) throw new Refused(await serverMessage(res));
  return res.json();
}

/** A delete that found nothing is a delete that was asked for: the row is
 * gone either way. */
export function goneOk(res: Response): boolean {
  return res.ok || res.status === 404;
}

/** Writes text to the clipboard and reports whether it took: in an insecure
 * context, or with the permission refused, a button saying "Copied" over a
 * clipboard that still holds whatever was there before is a plain lie. */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
