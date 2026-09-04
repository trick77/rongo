import type { ReactNode } from "react";
import { createLowlight, common } from "lowlight";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import type { Element, ElementContent, Root } from "hast";

/**
 * Syntax colouring for the two places code appears: fenced blocks in an
 * answer and the source viewer. One engine, one palette, so both look the
 * same. lowlight is highlight.js as a hast tree, which is what ../loom colours
 * with (via rehype-highlight); the hljs-* classes and its theme carry over.
 *
 * The tree is turned into React nodes here, never into HTML: the answer is
 * model output, and innerHTML would make a prompt injection a script tag.
 *
 * No auto-detection. An answer re-renders on every streamed token, and a
 * half-written snippet would change its detected language between deltas.
 * The language comes from the fence tag or the file name, or there is none.
 */

// `common` covers the corpus; dockerfile is the one grammar outside it that
// repos here carry (Containerfile).
const low = createLowlight(common);
low.register({ dockerfile });

/** languageOf returns the grammar name lowlight knows the tag under, or
 * null. lowlight throws on an unknown name, and one throw would take a whole
 * answer down. */
export function languageOf(tag: string | undefined | null): string | null {
  if (!tag) return null;
  const name = tag.toLowerCase();
  return low.registered(name) ? name : null;
}

const byExtension: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  py: "python",
  java: "java",
  kt: "kotlin",
  kts: "kotlin",
  rs: "rust",
  c: "c",
  h: "c",
  cc: "cpp",
  cpp: "cpp",
  hpp: "cpp",
  cs: "csharp",
  rb: "ruby",
  php: "php",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  sql: "sql",
  yaml: "yaml",
  yml: "yaml",
  json: "json",
  toml: "ini",
  xml: "xml",
  html: "xml",
  css: "css",
  md: "markdown",
  diff: "diff",
};

const byBasename: Record<string, string> = {
  makefile: "makefile",
  containerfile: "dockerfile",
  dockerfile: "dockerfile",
};

/** languageForPath picks the grammar for a file by its name. */
export function languageForPath(path: string): string | null {
  const base = path.slice(path.lastIndexOf("/") + 1);
  const dot = base.lastIndexOf(".");
  const hit =
    byBasename[base.toLowerCase()] ??
    (dot > 0 ? byExtension[base.slice(dot + 1).toLowerCase()] : undefined);
  return languageOf(hit);
}

function toNodes(children: ElementContent[], key: string): ReactNode[] {
  return children.map((n, i) => {
    if (n.type === "text") return n.value;
    if (n.type === "element") {
      const cls = n.properties?.className;
      const className = Array.isArray(cls) ? cls.join(" ") : undefined;
      return (
        <span key={`${key}-${i}`} className={className}>
          {toNodes(n.children, `${key}-${i}`)}
        </span>
      );
    }
    return null;
  });
}

/** highlightBlock colours one snippet. Without a language it is the text. */
export function highlightBlock(code: string, lang: string | null): ReactNode[] {
  if (!lang) return [code];
  return toNodes(low.highlight(lang, code).children as ElementContent[], "h");
}

/**
 * highlightLines colours a whole file once and hands back its lines, so the
 * viewer can keep its per-line grid (numbers, cited-range mark, anchors)
 * while a comment or raw string that spans lines keeps its colour past the
 * first one. The tree is split at every newline; the spans open at that
 * point are re-opened on the next line.
 */
export function highlightLines(code: string, lang: string | null): ReactNode[][] {
  if (!lang) return code.split("\n").map((l) => [l]);
  const tree: Root = low.highlight(lang, code);
  const lines: ElementContent[][] = [[]];

  // walk appends n to the current line, splitting text at newlines. `stack`
  // is the chain of open elements from the root to n's parent; a new line
  // continues inside fresh copies of that chain.
  function walk(n: ElementContent, stack: Element[]) {
    if (n.type === "text") {
      const parts = n.value.split("\n");
      parts.forEach((part, i) => {
        if (i > 0) lines.push(reopen(stack));
        if (part) tip(stack).push({ type: "text", value: part });
      });
      return;
    }
    if (n.type !== "element") return;
    const copy: Element = { ...n, children: [] };
    tip(stack).push(copy);
    stack.push(copy);
    for (const c of n.children) walk(c, stack);
    stack.pop();
  }

  // tip is the children array new nodes go into: the innermost open element
  // on the current line, or the line itself.
  function tip(stack: Element[]): ElementContent[] {
    return stack.length > 0 ? stack[stack.length - 1].children : lines[lines.length - 1];
  }

  // reopen builds the next line with empty copies of the open elements nested
  // in the same order, and rebinds the stack entries to those copies in place,
  // so everything still to come inside them lands on the new line.
  function reopen(stack: Element[]): ElementContent[] {
    const line: ElementContent[] = [];
    let parent = line;
    for (let i = 0; i < stack.length; i++) {
      const copy: Element = { ...stack[i], children: [] };
      parent.push(copy);
      stack[i] = copy;
      parent = copy.children;
    }
    return line;
  }

  for (const c of tree.children) walk(c as ElementContent, []);
  return lines.map((l, i) => toNodes(l, `l${i}`));
}
