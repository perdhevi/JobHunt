import { FormEvent, useState } from "react";
import { api, Application, Doc } from "./api";
import { FileField, FileLink, FileSlot, resolveSlot, slotFrom } from "./FileField";

const NAME = { cover: "cover letter", resume: "resume" };

export function Documents({ apps, docs, onClose, onChanged }: { apps: Application[]; docs: Doc[]; onClose: () => void; onChanged: () => void }) {
  const [kind, setKind] = useState<Doc["kind"]>("cover");
  const [editing, setEditing] = useState<Doc | null>(null);
  const [form, setForm] = useState({ name: "", use: "", body: "" });
  const [slot, setSlot] = useState<FileSlot>(slotFrom(null));
  const [msg, setMsg] = useState("");
  const [confirm, setConfirm] = useState(false);

  const start = (d: Doc | null) => {
    setEditing(d);
    setForm({ name: d?.name ?? "", use: d?.use ?? "", body: d?.body ?? "" });
    setSlot(slotFrom(d?.file ?? null));
    setConfirm(false);
    setMsg("");
  };

  const idOf = (a: Application, k: Doc["kind"]) => (k === "resume" ? a.resumeId : a.letterId);
  const stats = (d: Doc) => {
    const sent = apps.filter((a) => idOf(a, d.kind) === d.id && a.sent);
    const replies = sent.filter((a) => a.replied).length;
    return { sent: sent.length, replies, rate: sent.length ? Math.round((replies / sent.length) * 100) : 0 };
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const file = await resolveSlot(slot);
      await api.saveDoc({ id: editing?.id, kind: editing?.kind ?? kind, ...form, file });
      start(null);
      setMsg("Version saved.");
      onChanged();
    } catch (err) {
      setMsg(err instanceof Error ? err.message : "Couldn't save.");
    }
  };

  const k = editing?.kind ?? kind;
  const list = docs.filter((d) => d.kind === kind);
  const inUse = editing ? apps.filter((a) => idOf(a, editing.kind) === editing.id).length : 0;

  return (
    <div className="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="panel" role="dialog" aria-labelledby="lTitle">
        <div className="lhead"><h2 id="lTitle">Document library</h2><button type="button" className="btn" onClick={onClose}>Done</button></div>
        <p className="sub flush">Save each version of your cover letter and resume once, then tag applications with the versions you sent. Each version shows how often it got a reply.</p>
        <div className="seg" role="group" aria-label="Document type">
          {(["cover", "resume"] as const).map((x) => (
            <button
              key={x}
              type="button"
              aria-pressed={kind === x}
              onClick={() => {
                setKind(x);
                start(null);
              }}
            >
              {x === "cover" ? "Cover letters" : "Resumes"}
            </button>
          ))}
        </div>
        <div className="llist">
          {list.length === 0 && <p className="lempty">No {NAME[kind]} versions yet. Add your first one below.</p>}
          {list.map((d) => {
            const st = stats(d);
            return (
              <div className="litem" key={d.id}>
                <div className="top">
                  <b>{d.name}</b>
                  <span className="rate">sent {st.sent}×{st.sent ? ` · ${st.replies} repl${st.replies === 1 ? "y" : "ies"} (${st.rate}%)` : ""}</span>
                </div>
                {d.use && <div className="use">{d.use}</div>}
                {d.body && <div className="pv">{d.body}</div>}
                {d.file && <div className="fileline"><FileLink f={d.file} /></div>}
                <div className="btns">
                  <button type="button" className="btn" onClick={() => navigator.clipboard.writeText(d.body).catch(() => {})}>Copy text</button>
                  <button type="button" className="btn" onClick={() => start(d)}>Edit</button>
                </div>
              </div>
            );
          })}
        </div>
        <form className="lform" onSubmit={submit}>
          <h3>{editing ? "Edit" : "New"} {NAME[k]} version</h3>
          <div className="f"><label htmlFor="l-name">Version name</label><input id="l-name" required placeholder="e.g. AI Agent Eng v2 – short" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></div>
          <div className="f"><label htmlFor="l-use">Best for</label><input id="l-use" placeholder="e.g. Full-time agent/LLM roles" value={form.use} onChange={(e) => setForm({ ...form, use: e.target.value })} /></div>
          <div className="f"><label htmlFor="l-file">File (PDF, TXT or MD)</label><FileField id="l-file" slot={slot} onChange={setSlot} /></div>
          <div className="f">
            <label htmlFor="l-body">{k === "resume" ? "Summary or notes" : "Letter text"}</label>
            <textarea id="l-body" className="tall" placeholder={k === "resume" ? "What this version emphasises." : "Paste the base letter. Use [Company] and [Role] as placeholders."} value={form.body} onChange={(e) => setForm({ ...form, body: e.target.value })} />
          </div>
          {confirm && editing && (
            <div className="confirm">
              <span>Delete "{editing.name}"?{inUse ? ` ${inUse} application${inUse > 1 ? "s" : ""} will show it as a deleted version.` : ""}</span>
              <button
                type="button"
                className="btn"
                onClick={async () => {
                  await api.deleteDoc(editing.id).catch((err) => setMsg(err.message));
                  start(null);
                  onChanged();
                }}
              >
                Delete
              </button>
              <button type="button" className="btn ghost" onClick={() => setConfirm(false)}>Keep</button>
            </div>
          )}
          {msg && <p className="status" aria-live="polite">{msg}</p>}
          <div className="actions">
            {editing ? <button type="button" className="btn ghost danger" onClick={() => setConfirm(true)}>Delete version</button> : <span />}
            <div className="r">
              {editing && <button type="button" className="btn" onClick={() => start(null)}>Cancel edit</button>}
              <button type="submit" className="btn primary">Save version</button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
