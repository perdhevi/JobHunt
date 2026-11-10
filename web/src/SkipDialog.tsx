import { FormEvent, useEffect, useRef, useState } from "react";
import { Skip, SKIP_REASONS } from "./api";

// SkipDialog asks why a job is being skipped. "Other" needs details; any
// reason can carry them.
export function SkipDialog({ title, initial, onSkip, onCancel }: { title: string; initial?: Skip; onSkip: (s: Skip) => void; onCancel: () => void }) {
  const [reason, setReason] = useState(initial?.reason ?? "");
  const [note, setNote] = useState(initial?.note ?? "");
  const [err, setErr] = useState("");
  const noteRef = useRef<HTMLTextAreaElement>(null);
  const formRef = useRef<HTMLFormElement>(null);

  useEffect(() => {
    formRef.current?.querySelector<HTMLElement>("[role=radio]")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") return onCancel();
      // Keep Tab inside the dialog, so a key press can't reach the page behind it.
      if (e.key !== "Tab" || !formRef.current) return;
      const items = [...formRef.current.querySelectorAll<HTMLElement>("button, textarea")];
      const i = items.indexOf(document.activeElement as HTMLElement);
      const next = e.shiftKey ? (i <= 0 ? items.length - 1 : i - 1) : i === items.length - 1 ? 0 : i + 1;
      e.preventDefault();
      items[next]?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!reason) return setErr("Pick a reason.");
    if (reason === "other" && !note.trim()) {
      noteRef.current?.focus();
      return setErr("Say why in the details when the reason is Other.");
    }
    onSkip({ reason, note: note.trim() });
  };

  return (
    <div className="scrim center" onClick={(e) => e.target === e.currentTarget && onCancel()}>
      <form ref={formRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="skipTitle" onSubmit={submit}>
        <h2 id="skipTitle">Skip {title}?</h2>
        <p className="sub">Why aren't you applying? It stays on record, so the same job and its reposts aren't picked up again.</p>
        <div className="outcomes" role="radiogroup" aria-label="Reason">
          {SKIP_REASONS.map((r) => (
            <button
              key={r.key}
              type="button"
              role="radio"
              aria-checked={reason === r.key}
              className={"btn" + (reason === r.key ? " picked" : "")}
              onClick={() => {
                setReason(r.key);
                setErr("");
                if (r.key === "other") setTimeout(() => noteRef.current?.focus(), 0);
              }}
            >
              {r.label}
            </button>
          ))}
        </div>
        <div className="f">
          <label htmlFor="skipNote">Details{reason === "other" ? "" : " (optional)"}</label>
          <textarea id="skipNote" ref={noteRef} placeholder="e.g. max $35/h, needs US work authorization, on-call in US hours" value={note} onChange={(e) => (setNote(e.target.value), setErr(""))} />
        </div>
        {err && <p className="status err flush" aria-live="polite">{err}</p>}
        <div className="actions end">
          <button type="button" className="btn ghost" onClick={onCancel}>Cancel</button>
          <button type="submit" className="btn primary">Skip</button>
        </div>
      </form>
    </div>
  );
}
