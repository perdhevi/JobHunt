import { FormEvent, useEffect, useRef, useState } from "react";
import { api, Evidence, EVIDENCE_KINDS, fmtDay, kindLabel, SkillRow } from "./api";
import { webLink } from "./Board";

const blank = (skill: string): Evidence => ({ id: "", skill, kind: "project", title: "", url: "", date: "", note: "" });

const STATUS_TEXT = {
  proven: "In your profile as a skill you have, with proof.",
  claimed: "In your profile as a skill you have, but nothing here backs it up yet. Add proof, or an employer only has your word for it.",
  learning: "In your profile as a skill you're learning. Add proof as you go, and set a goal.",
  gap: "Not in your profile, so it counts as a gap. Set a goal for closing it, or add proof if you already have the skill.",
} as const;

// SkillPanel shows and edits the proof and the goal for one skill.
export function SkillPanel({ row, onClose, onChanged }: { row: SkillRow; onClose: () => void; onChanged: () => void }) {
  const [items, setItems] = useState<Evidence[] | null>(null);
  const [goal, setGoal] = useState(row.goal ?? "");
  const [due, setDue] = useState(row.due ?? "");
  const [draft, setDraft] = useState<Evidence | null>(null);
  const [msg, setMsg] = useState({ text: "", err: false });
  const first = useRef<HTMLButtonElement>(null);

  const load = async () => {
    try {
      const r = await api.evidence();
      setItems(r.evidence.filter((e) => e.skill === row.skill));
    } catch (e) {
      setMsg({ text: e instanceof Error ? e.message : "Couldn't load the proof.", err: true });
    }
  };
  useEffect(() => {
    load();
    first.current?.focus();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [row.skill]);

  const proven = row.status === "have" ? ((items?.length ?? row.proof) > 0 ? "proven" : "claimed") : row.status;

  const saveGoal = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const g = await api.setGoal({ skill: row.skill, goal, due: goal.trim() ? due : "" });
      setGoal(g.goal ?? "");
      setDue(g.due ?? "");
      setMsg({ text: g.goal ? "Goal saved." : "Goal cleared.", err: false });
      onChanged();
    } catch (err) {
      setMsg({ text: err instanceof Error ? err.message : "Couldn't save the goal.", err: true });
    }
  };

  const saveItem = async (e: FormEvent) => {
    e.preventDefault();
    if (!draft) return;
    try {
      await api.saveEvidence(draft);
      setDraft(null);
      setMsg({ text: "Proof saved.", err: false });
      await load();
      onChanged();
    } catch (err) {
      setMsg({ text: err instanceof Error ? err.message : "Couldn't save.", err: true });
    }
  };

  const remove = async (ev: Evidence) => {
    try {
      await api.deleteEvidence(ev.id);
      setMsg({ text: `Removed "${ev.title}".`, err: false });
      await load();
      onChanged();
    } catch (err) {
      setMsg({ text: err instanceof Error ? err.message : "Couldn't remove it.", err: true });
    }
  };

  const set = <K extends keyof Evidence>(k: K, v: Evidence[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));

  return (
    <div className="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="panel" role="dialog" aria-modal="true" aria-labelledby="skTitle">
        <div className="lhead">
          <h2 id="skTitle">{row.skill}</h2>
          <button ref={first} type="button" className="btn" onClick={onClose}>Done</button>
        </div>
        <div className={"proofline " + proven}>
          <span className={"proofchip " + proven}>{PROOF_LABEL[proven]}</span>
          <span className="hint">
            {row.count > 0 ? `Asked for in ${row.share}% of recent relevant listings (${row.count}).` : "No recent listing asked for it."} {row.area}
          </span>
        </div>
        <p className="hint flush">
          {STATUS_TEXT[proven]} Whether it counts as have, learning or a gap comes from the profile under Radar &gt; Settings.
        </p>

        <section className="skbox">
          <h3>Proof</h3>
          {items === null ? (
            <p className="lempty">Loading…</p>
          ) : items.length === 0 ? (
            <p className="lempty">Nothing yet. Strongest first: something running people can try, a public repo, a write-up of a problem you solved, a certificate, a described piece of work.</p>
          ) : (
            <div className="llist">
              {items.map((ev) => {
                const link = webLink(ev.url);
                return (
                  <div key={ev.id} className="litem">
                    <div className="top">
                      <b>{link ? <a href={link} target="_blank" rel="noopener noreferrer">{ev.title}</a> : ev.title}</b>
                      <span className="rate">{kindLabel(ev.kind)}{ev.date ? ` · ${fmtDay(ev.date)}` : ""}</span>
                    </div>
                    {ev.note && <div className="pv">{ev.note}</div>}
                    <div className="btns">
                      <button type="button" className="btn" onClick={() => setDraft({ ...ev })}>Edit</button>
                      <button type="button" className="btn ghost danger" onClick={() => remove(ev)}>Remove</button>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
          {draft ? (
            <form className="lform" onSubmit={saveItem}>
              <h3>{draft.id ? "Edit proof" : "Add proof"}</h3>
              <div className="fgrid">
                <div className="f">
                  <label htmlFor="ev-kind">Kind</label>
                  <select id="ev-kind" value={draft.kind} onChange={(e) => set("kind", e.target.value)}>
                    {EVIDENCE_KINDS.map((k) => <option key={k.key} value={k.key}>{k.label}</option>)}
                  </select>
                </div>
                <div className="f">
                  <label htmlFor="ev-date">When</label>
                  <input id="ev-date" type="date" value={draft.date} onChange={(e) => set("date", e.target.value)} />
                </div>
                <div className="f full">
                  <label htmlFor="ev-title">Title</label>
                  <input id="ev-title" required autoFocus placeholder="e.g. GitHub Actions CI for JobHunt" value={draft.title} onChange={(e) => set("title", e.target.value)} />
                </div>
                <div className="f full">
                  <label htmlFor="ev-url">Link</label>
                  <input id="ev-url" type="url" placeholder="https://" value={draft.url} onChange={(e) => set("url", e.target.value)} />
                </div>
                <div className="f full">
                  <label htmlFor="ev-note">What it shows (optional)</label>
                  <textarea id="ev-note" placeholder="One or two lines you could paste into an application: what you built, the result, numbers if you have them." value={draft.note} onChange={(e) => set("note", e.target.value)} />
                </div>
              </div>
              <div className="actions">
                <button type="button" className="btn ghost" onClick={() => setDraft(null)}>Cancel</button>
                <button type="submit" className="btn primary">Save proof</button>
              </div>
            </form>
          ) : (
            <div className="actions">
              <span />
              <button type="button" className="btn primary" onClick={() => setDraft(blank(row.skill))}>+ Add proof</button>
            </div>
          )}
        </section>

        <form className="skbox" onSubmit={saveGoal}>
          <h3>Goal</h3>
          <div className="fgrid">
            <div className="f full">
              <label htmlFor="sk-goal">What you'll do</label>
              <input id="sk-goal" placeholder={row.status === "gap" ? "e.g. Deploy jobhunt on k3s with manifests in the repo" : "e.g. Write up how the Radar scores listings"} value={goal} onChange={(e) => setGoal(e.target.value)} />
            </div>
            <div className="f">
              <label htmlFor="sk-due">By</label>
              <input id="sk-due" type="date" value={due} onChange={(e) => setDue(e.target.value)} />
            </div>
          </div>
          <div className="actions">
            <span className="hint">Leave it empty and save to clear it.</span>
            <button type="submit" className="btn">Save goal</button>
          </div>
        </form>

        {msg.text && <p className={"status flush" + (msg.err ? " err" : "")} aria-live="polite">{msg.text}</p>}
      </div>
    </div>
  );
}

export const PROOF_LABEL = { proven: "✓ Proven", claimed: "Claimed, no proof", learning: "Learning", gap: "Gap" } as const;
