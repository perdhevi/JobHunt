import { useState } from "react";
import { api, FileMeta, fileUrl, kb } from "./api";

const ACCEPT = ".pdf,.txt,.md,application/pdf,text/plain,text/markdown";

export interface FileSlot {
  keep: FileMeta | null;
  pending: File | null;
}

export const slotFrom = (keep: FileMeta | null): FileSlot => ({ keep, pending: null });

// Uploads a pending file; the record write that follows links it.
export async function resolveSlot(s: FileSlot): Promise<FileMeta | null> {
  return s.pending ? api.upload(s.pending) : s.keep;
}

export function FileLink({ f }: { f: FileMeta }) {
  return (
    <>
      <a href={fileUrl(f)} target="_blank" rel="noopener noreferrer">{f.name}</a> <span className="sz">{f.size ? kb(f.size) : ""}</span>
    </>
  );
}

export function FileField({ id, slot, onChange, inherited }: { id: string; slot: FileSlot; onChange: (s: FileSlot) => void; inherited?: FileMeta | null }) {
  const [err, setErr] = useState("");
  const [inputKey, setInputKey] = useState(0);
  const clear = () => {
    onChange({ keep: slot.pending ? slot.keep : null, pending: null });
    setInputKey((k) => k + 1);
  };
  return (
    <>
      {slot.pending ? (
        <div className="fileline pending">
          <span>{slot.pending.name}</span>
          <span className="sz">{kb(slot.pending.size)} · uploads when you save</span>
          <button type="button" className="btn" onClick={clear}>Remove</button>
        </div>
      ) : slot.keep ? (
        <div className="fileline">
          <FileLink f={slot.keep} />
          <button type="button" className="btn" onClick={clear}>Remove</button>
        </div>
      ) : inherited ? (
        <div className="fileline"><span className="hint">Using the version's file:</span> <FileLink f={inherited} /></div>
      ) : null}
      <input
        key={inputKey}
        id={id}
        type="file"
        accept={ACCEPT}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (!f) return;
          const ext = f.name.split(".").pop()?.toLowerCase() ?? "";
          if (!["pdf", "txt", "md"].includes(ext)) setErr(`"${f.name}" can't be stored here. Save it as PDF, TXT or MD.`);
          else if (f.size > 20 * 1048576) setErr(`"${f.name}" is over 20 MB.`);
          else {
            setErr("");
            onChange({ ...slot, pending: f });
            return;
          }
          setInputKey((k) => k + 1);
        }}
      />
      {err && <span className="status err">{err}</span>}
    </>
  );
}
