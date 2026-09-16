import { useEffect, useRef, useState } from "react";

import { Chevron } from "./icons";
import { useMenuPlacement } from "./menuPlacement";
import { languages } from "./turns";

/**
 * The list, a component of its own for the same reason ThreadMenu is one: the
 * placement hook measures on mount, so it has to mount WITH the list, not
 * with the pill that was there all along.
 */
function LanguageList({
  value,
  active,
  onActive,
  onChoose,
}: {
  value: string;
  active: number;
  onActive: (i: number) => void;
  onChoose: (code: string) => void;
}) {
  const { menuRef, verticalClass } = useMenuPlacement();
  return (
    <div
      ref={menuRef}
      id="lang-listbox"
      role="listbox"
      aria-labelledby="lang-label"
      // A press on a row must not take focus off the pill: the pill's blur
      // closes the list, and the click would land on rows already gone.
      onMouseDown={(e) => e.preventDefault()}
      className={
        "absolute left-0 z-20 w-[168px] overflow-hidden rounded-ui border border-elevated-border bg-elevated py-1 shadow-menu " +
        verticalClass
      }
    >
      {languages.map((l, i) => (
        <div
          key={l.code}
          id={"lang-option-" + l.code}
          role="option"
          aria-selected={l.code === value}
          onClick={() => onChoose(l.code)}
          onPointerMove={() => onActive(i)}
          className={
            "mx-1 flex min-h-[30px] w-[calc(100%-0.5rem)] cursor-pointer items-center rounded-md px-3 py-1 text-sm/5 text-elevated-ink transition-colors " +
            (i === active ? "bg-elevated-hover " : "") +
            (l.code === value ? "font-medium" : "")
          }
        >
          {l.name}
        </div>
      ))}
    </div>
  );
}

/**
 * The answer-language pill and its list. Not a native <select>: on Windows
 * the browser's option popup is a window of its own, outside the page. CSS
 * cannot keep it inside the viewport, cannot turn it upward, and Chromium
 * paints it from a sliver of the option styles, so Edge drew a grey,
 * see-through list that hung below the browser's edge. This list is page
 * DOM, on the thread menu's surface, placed by the thread menu's hook.
 */
export default function LanguageSelect({ value, onChange }: { value: string; onChange: (code: string) => void }) {
  const [open, setOpen] = useState(false);
  // The row the keyboard is on while the list is open; the pointer ignores it.
  const [active, setActive] = useState(0);
  const buttonRef = useRef<HTMLButtonElement>(null);

  const current = languages.find((l) => l.code === value) ?? languages[0];

  function show() {
    setActive(Math.max(0, languages.findIndex((l) => l.code === value)));
    setOpen(true);
  }

  function choose(code: string) {
    onChange(code);
    setOpen(false);
    buttonRef.current?.focus();
  }

  // Closes on a pointer anywhere but the list and the pill, and on Escape.
  // The pill is spared because its own click toggles, and closing here first
  // would make the toggle reopen the list that was just dismissed.
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      const target = e.target;
      if (target instanceof Element && target.closest('[role="listbox"], [aria-haspopup="listbox"]')) return;
      setOpen(false);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") {
        setOpen(false);
        buttonRef.current?.focus();
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) {
        show();
        return;
      }
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => Math.min(languages.length - 1, Math.max(0, i + step)));
    } else if (e.key === "Home" && open) {
      e.preventDefault();
      setActive(0);
    } else if (e.key === "End" && open) {
      e.preventDefault();
      setActive(languages.length - 1);
    } else if (e.key === "Enter" && open) {
      e.preventDefault();
      choose(languages[active].code);
    } else if (e.key === " ") {
      // Space is handled here in full, and its keyup below is swallowed:
      // Chromium clicks a button on Space's keyup, Firefox on keyup even when
      // the keydown was cancelled, and either click would toggle the list a
      // second time right after the choice.
      e.preventDefault();
      if (open) choose(languages[active].code);
      else show();
    }
  }

  // Tab out of the pill closes the list without a choice; the pointer on a row
  // is not a focus change (see the list's onMouseDown), so this only fires when
  // focus really left.
  function onBlur(e: React.FocusEvent<HTMLDivElement>) {
    if (open && !e.currentTarget.contains(e.relatedTarget)) setOpen(false);
  }

  return (
    <div className="relative" onBlur={onBlur}>
      {/* Its box is the Role toggle's, off the same rule: the same border and
          radius, p-0.5 on the pill, py-1 text-xs on the control inside it.
          Height is content-driven, so the two agree wherever the text does,
          the pointer-coarse size included. The arrow is drawn here. */}
      {/* A select-only combobox, as ARIA's pattern has it: the pill holds
          focus, its content is the value, the label is its name, and the
          active row is announced from here, since the list never has focus.
          aria-activedescendant is ignored on a plain button, and an aria-label
          would replace the value with the name. */}
      <span id="lang-label" className="sr-only">
        Answer language
      </span>
      <button
        ref={buttonRef}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby="lang-label"
        aria-controls={open ? "lang-listbox" : undefined}
        aria-activedescendant={open ? "lang-option-" + languages[active].code : undefined}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKeyDown}
        onKeyUp={(e) => {
          if (e.key === " ") e.preventDefault();
        }}
        className={
          "relative inline-flex cursor-pointer items-center rounded-full border border-border bg-bg p-0.5 text-xs " +
          "outline-none focus-visible:ring-2 focus-visible:ring-accent-dim " +
          (open ? "border-elevated-border text-ink" : "text-muted hover:border-elevated-border hover:text-ink")
        }
      >
        <span className="py-1 pr-6 pl-3 font-medium pointer-coarse:text-base">{current.name}</span>
        <span className="pointer-events-none absolute right-2">
          {/* Down at rest as the select's was; up while the list is out. */}
          <Chevron open up={open} />
        </span>
      </button>
      {open && <LanguageList value={value} active={active} onActive={setActive} onChoose={choose} />}
    </div>
  );
}
