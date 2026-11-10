import { FormEvent, useEffect, useRef, useState } from "react";
import { api, ApiError, Application, CheckResult, CreateResult, Doc, OUTCOMES, SKIP_REASONS, skipLabel, SOURCES, STAGES, taggedEmail, today } from "./api";
import { FileField, FileSlot, resolveSlot, slotFrom } from "./FileField";

type Draft = Pick<Application, "company" | "role" | "track" | "source" | "stage" | "outcome" | "applied" | "comp" | "nextStep" | "nextDate" | "contact" | "url" | "letterId" | "letterText" | "resumeId" | "notes" | "skipReason" | "skipNote" | "skipped">;

const blank: Draft = { company: "", role: "", track: "fulltime", source: "LinkedIn", stage: "applied", outcome: "", applied: today(), comp: "", nextStep: "", nextDate: "", contact: "", url: "", letterId: "", letterText: "", resumeId: "", notes: "", skipReason: "", skipNote: "", skipped: "" };

interface Props {
  app: Application | null;
  docs: Doc[];
  email: string;
  onClose: () => void;
  onOpenOther: (id: string) => void;
  onSaved: (msg: string) => void;
  onChanged: () => void;
}

export function Editor({ app, docs, email, onClose, onOpenOther, onSaved, onChanged }: Props) {
  const [draft, setDraft] = useState<Draft>(app ? pick(app) : blank);
  const [cover, setCover] = useState<FileSlot>(slotFrom(app?.coverFile ?? null));
  const [resume, setResume] = useState<FileSlot>(slotFrom(app?.resumeFile ?? null));
  const [check, setCheck] = useState<CheckResult | null>(null);
  const [blocked, setBlocked] = useState(false); // save was refused; offer "add anyway"
  const [msg, setMsg] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirmDel, setConfirmDel] = useState(false);
  const first = useRef<HTMLInputElement>(null);
  // The version the form was opened on, so a save can detect an agent's edit.
  const version = useRef(app?.version ?? 0);

  useEffect(() => first.current?.focus(), []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }));

  const liveCheck = async () => {
    if (app || (!draft.url && !(draft.company && draft.role))) return;
    try {
      setCheck(await api.check(draft.company, draft.role, draft.url));
    } catch {
      /* the save path checks again */
    }
  };

  const save = async (force: boolean) => {
    setSaving(true);
    setMsg("");
    try {
      const [coverFile, resumeFile] = [await resolveSlot(cover), await resolveSlot(resume)];
      const body = { ...draft, coverFile, resumeFile };
      // Keep uploaded ids so a retry after a refusal doesn't upload twice.
      setCover(slotFrom(coverFile));
      setResume(slotFrom(resumeFile));
      if (app) {
        await api.updateApp({ ...body, id: app.id, version: version.current }, force);
        onSaved("Saved.");
      } else {
        await api.createApp(body, force);
        onSaved("Added.");
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        const res = e.body as Partial<CreateResult> | null;
        if (res?.check) {
          setCheck(res.check);
          setBlocked(true);
        } else if (e.message.startsWith("record changed")) {
          setMsg("This record changed while you were editing, probably from an agent. Close it and reopen to see the latest.");
        } else {
          setMsg(e.message);
        }
      } else {
        setMsg(e instanceof Error ? e.message : "Couldn't save.");
      }
    } finally {
      setSaving(false);
    }
  };

  const covers = docs.filter((d) => d.kind === "cover");
  const resumes = docs.filter((d) => d.kind === "resume");
  const showCheck = !app && check && check.verdict !== "new";

  return (
    <div className="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <form
        className="panel"
        aria-labelledby="formTitle"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          save(false);
        }}
      >
        <h2 id="formTitle">{app ? "Edit application" : "Add application"}</h2>
        {app && <RefBox ref_={app.ref} email={email} />}

        {showCheck && (
          <div className={"dupbox " + (check.verdict === "already_sent" || check.verdict === "skipped" ? "bad" : "warn")} role="alert">
            <b>{check.message}</b>
            <ul>
              {check.matches.map((m) => (
                <li key={m.id}>
                  <button type="button" className="linkbtn" onClick={() => onOpenOther(m.id)}>{m.company}, {m.role}</button>
                  <span className="hint">
                    {" "}
                    {STAGES.find((s) => s.id === m.stage)?.name}
                    {m.applied ? `, applied ${m.applied}` : ""}
                    {m.stage === "skipped" && m.skipReason ? `: ${skipLabel(m.skipReason)}${m.skipNote ? ` (${m.skipNote})` : ""}` : ""} · matched on {m.matchedOn.replace("_", " ")}
                  </span>
                </li>
              ))}
            </ul>
            {blocked && <button type="button" className="btn" disabled={saving} onClick={() => save(true)}>Add anyway</button>}
          </div>
        )}

        <div className="fgrid">
          <Field id="company" label="Company / client"><input id="company" ref={first} required value={draft.company} onChange={(e) => set("company", e.target.value)} onBlur={liveCheck} /></Field>
          <Field id="role" label="Role"><input id="role" required value={draft.role} onChange={(e) => set("role", e.target.value)} onBlur={liveCheck} /></Field>
          <Field id="track" label="Track">
            <select id="track" value={draft.track} onChange={(e) => set("track", e.target.value as Draft["track"])}>
              <option value="fulltime">Full-time</option>
              <option value="contract">Contract / Upwork</option>
            </select>
          </Field>
          <Field id="source" label="Source">
            <select id="source" value={draft.source} onChange={(e) => set("source", e.target.value)}>
              {[...new Set([...SOURCES, draft.source].filter(Boolean))].map((s) => <option key={s}>{s}</option>)}
            </select>
          </Field>
          <Field id="stage" label="Stage">
            <select id="stage" value={draft.stage} onChange={(e) => set("stage", e.target.value)}>
              {STAGES.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </Field>
          <Field id="outcome" label="Outcome (if closed)">
            <select
              id="outcome"
              value={draft.outcome}
              onChange={(e) => setDraft((d) => ({ ...d, outcome: e.target.value, stage: e.target.value ? "closed" : d.stage }))}
            >
              <option value="">—</option>
              {[...new Set([...OUTCOMES, draft.outcome].filter(Boolean))].map((o) => <option key={o}>{o}</option>)}
            </select>
          </Field>
          {draft.stage === "skipped" && (
            <fieldset className="sent skip">
              <legend>Why it was skipped</legend>
              <Field id="skipReason" label="Reason">
                <select id="skipReason" required value={draft.skipReason} onChange={(e) => set("skipReason", e.target.value)}>
                  <option value="">Choose…</option>
                  {SKIP_REASONS.map((r) => <option key={r.key} value={r.key}>{r.label}</option>)}
                </select>
              </Field>
              <Field id="skipNote" label={draft.skipReason === "other" ? "Details" : "Details (optional)"}>
                <textarea id="skipNote" required={draft.skipReason === "other"} placeholder="e.g. max $35/h, needs US work authorization" value={draft.skipNote} onChange={(e) => set("skipNote", e.target.value)} />
              </Field>
              {draft.skipped && <span className="hint">Skipped on {draft.skipped}.</span>}
            </fieldset>
          )}
          <Field id="applied" label="Date applied"><input id="applied" type="date" value={draft.applied} onChange={(e) => set("applied", e.target.value)} /></Field>
          <Field id="comp" label="Salary / rate"><input id="comp" placeholder="e.g. $30/hr, $70–85k" value={draft.comp} onChange={(e) => set("comp", e.target.value)} /></Field>
          <Field id="nextStep" label="Next step" full><input id="nextStep" placeholder="e.g. Send follow-up to recruiter" value={draft.nextStep} onChange={(e) => set("nextStep", e.target.value)} /></Field>
          <Field id="nextDate" label="Next step due"><input id="nextDate" type="date" value={draft.nextDate} onChange={(e) => set("nextDate", e.target.value)} /></Field>
          <Field id="contact" label="Contact"><input id="contact" placeholder="Name, title" value={draft.contact} onChange={(e) => set("contact", e.target.value)} /></Field>
          <Field id="url" label="Job post link" full><input id="url" type="url" placeholder="https://" value={draft.url} onChange={(e) => set("url", e.target.value)} onBlur={liveCheck} /></Field>

          <fieldset className="sent">
            <legend>Cover letter sent</legend>
            <Field id="letter" label="Version"><VersionSelect id="letter" list={covers} value={draft.letterId} onChange={(v) => set("letterId", v)} /></Field>
            <Field id="coverFile" label="File (PDF, TXT or MD)"><FileField id="coverFile" slot={cover} onChange={setCover} inherited={docs.find((d) => d.id === draft.letterId)?.file} /></Field>
            <Field id="letterText" label="Text as sent (optional)"><textarea id="letterText" placeholder="Paste the final tailored text if you changed the base version for this company." value={draft.letterText} onChange={(e) => set("letterText", e.target.value)} /></Field>
          </fieldset>
          <fieldset className="sent">
            <legend>Resume sent</legend>
            <Field id="resume" label="Version"><VersionSelect id="resume" list={resumes} value={draft.resumeId} onChange={(v) => set("resumeId", v)} /></Field>
            <Field id="resumeFile" label="File (PDF, TXT or MD)"><FileField id="resumeFile" slot={resume} onChange={setResume} inherited={docs.find((d) => d.id === draft.resumeId)?.file} /></Field>
          </fieldset>
          <Field id="notes" label="Notes" full><textarea id="notes" placeholder="Interview questions, stack, what they care about…" value={draft.notes} onChange={(e) => set("notes", e.target.value)} /></Field>
        </div>

        {confirmDel && app && (
          <div className="confirm">
            <span>Delete {app.company} for good?</span>
            <button
              type="button"
              className="btn"
              onClick={async () => {
                try {
                  await api.deleteApp(app.id);
                  onSaved("Deleted.");
                } catch (e) {
                  setMsg(e instanceof Error ? e.message : "Couldn't delete.");
                  onChanged();
                }
              }}
            >
              Delete
            </button>
            <button type="button" className="btn ghost" onClick={() => setConfirmDel(false)}>Keep</button>
          </div>
        )}
        {msg && <p className="status err" aria-live="polite">{msg}</p>}
        <div className="actions">
          {app ? <button type="button" className="btn ghost danger" onClick={() => setConfirmDel(true)}>Delete</button> : <span />}
          <div className="r">
            <button type="button" className="btn" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn primary" disabled={saving}>{saving ? "Saving…" : "Save"}</button>
          </div>
        </div>
      </form>
    </div>
  );
}

function pick(a: Application): Draft {
  const { company, role, track, source, stage, outcome, applied, comp, nextStep, nextDate, contact, url, letterId, letterText, resumeId, notes, skipReason, skipNote, skipped } = a;
  return { company, role, track, source, stage, outcome, applied, comp, nextStep, nextDate, contact, url, letterId, letterText, resumeId, notes, skipReason, skipNote, skipped };
}

function Field({ id, label, full, children }: { id: string; label: string; full?: boolean; children: React.ReactNode }) {
  return (
    <div className={"f" + (full ? " full" : "")}>
      <label htmlFor={id}>{label}</label>
      {children}
    </div>
  );
}

function VersionSelect({ id, list, value, onChange }: { id: string; list: Doc[]; value: string; onChange: (v: string) => void }) {
  return (
    <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">None / not tracked</option>
      {list.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
      {value && !list.some((d) => d.id === value) && <option value={value}>(deleted version)</option>}
    </select>
  );
}

function RefBox({ ref_, email }: { ref_: string; email: string }) {
  const [copied, setCopied] = useState(false);
  const addr = taggedEmail(email, ref_);
  if (!addr) {
    return (
      <div className="refbox">
        <span className="lbl">Reference code</span>
        <code>{ref_}</code>
        <span className="hint">Add your base email in Settings to get a per-application address.</span>
      </div>
    );
  }
  const [local, domain] = addr.split("@");
  return (
    <div className="refbox">
      <span className="lbl">Reference email for this application</span>
      <div className="addr">
        <code>{local.split("+")[0]}+<b>{ref_}</b>@{domain}</code>
        <button
          type="button"
          className="btn"
          onClick={async () => {
            await navigator.clipboard.writeText(addr).catch(() => {});
            setCopied(true);
            setTimeout(() => setCopied(false), 1600);
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <span className="hint">Use it on the application form. Replies land in your inbox, and pasting the address into search finds this record.</span>
    </div>
  );
}
