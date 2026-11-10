import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, CreateResult, fmtDay, Lead, RadarSettings, RadarState, Skip, skipLabel, SkillReport, STAGES, today, daysSince } from "./api";
import { webLink } from "./Board";
import { SkipDialog } from "./SkipDialog";

type Tab = "leads" | "skills" | "settings";
type Sort = "score" | "date";
type Period = "1" | "7" | "all";

interface Props {
  onOpenApp: (id: string) => void;
  onChanged: () => void;
  onStatus: (msg: string, err?: boolean) => void;
}

function pref<T extends string>(key: string, allowed: readonly T[], def: T): T {
  try {
    const v = localStorage.getItem(key) as T | null;
    return v && allowed.includes(v) ? v : def;
  } catch {
    return def;
  }
}

function keep(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* storage may be blocked */
  }
}

const stageName = (id: string) => STAGES.find((s) => s.id === id)?.name ?? id;

export function Radar({ onOpenApp, onChanged, onStatus }: Props) {
  const [state, setState] = useState<RadarState | null>(null);
  const [err, setErr] = useState("");
  const [tab, setTab] = useState<Tab>(() => pref("jh-radar-tab", ["leads", "skills", "settings"] as const, "leads"));
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<Sort>(() => pref("jh-radar-sort", ["score", "date"] as const, "score"));
  const [period, setPeriod] = useState<Period>(() => pref("jh-radar-period", ["1", "7", "all"] as const, "all"));
  const [hideFlagged, setHideFlagged] = useState(() => pref("jh-radar-flagged", ["1", "0"] as const, "0") === "1");
  const [showTracked, setShowTracked] = useState(false);
  const [skipping, setSkipping] = useState<Lead | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // uid being acted on
  const [conflict, setConflict] = useState<{ uid: string; res: CreateResult } | null>(null);

  const load = useCallback(async () => {
    try {
      setState(await api.radar());
      setErr("");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Can't reach the server.");
    }
  }, []);

  // Poll quickly while a scan runs, slowly otherwise (an agent may act on leads).
  const running = state?.running ?? false;
  useEffect(() => {
    load();
    const t = setInterval(() => !document.hidden && load(), running ? 2000 : 15000);
    const onVisible = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [load, running]);

  // When a scan finishes, say how it went.
  const [wasRunning, setWasRunning] = useState(false);
  useEffect(() => {
    if (wasRunning && !running && state?.last) {
      const l = state.last;
      onStatus(l.error ? `Radar run failed: ${l.error}` : `Radar: ${l.matches} new lead${l.matches === 1 ? "" : "s"} from ${l.scanned} new listings.`, !!l.error);
    }
    setWasRunning(running);
  }, [running, state, wasRunning, onStatus]);

  const run = async () => {
    try {
      setState(await api.radarRun());
    } catch (e) {
      onStatus(e instanceof Error ? e.message : "Couldn't start the radar.", true);
      load();
    }
  };

  const act = async (l: Lead, stage: "saved" | "applied" | "skipped", skip?: Skip, force = false) => {
    setBusy(l.uid);
    setConflict(null);
    try {
      const res = await api.radarAct(l.uid, stage, skip, force);
      const verb = stage === "saved" ? "saved to the board" : stage === "applied" ? "marked applied" : `skipped (${skipLabel(skip?.reason ?? "")})`;
      onStatus(`${l.company}, ${l.title}: ${verb}.`);
      if (res.application) onChanged();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && (e.body as CreateResult | null)?.check) {
        setConflict({ uid: l.uid, res: e.body as CreateResult });
      } else {
        onStatus(e instanceof Error ? e.message : "Couldn't save that.", true);
      }
    } finally {
      setBusy(null);
      load();
    }
  };

  const leads = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = (state?.leads ?? []).filter((l) => {
      if (l.application && !showTracked) return false;
      if (hideFlagged && l.flags.length) return false;
      if (period !== "all") {
        const d = daysSince(l.firstSeen);
        if (d == null || d >= Number(period)) return false;
      }
      return !q || [l.title, l.company, l.location, l.sourceName, ...l.hits].join(" ").toLowerCase().includes(q);
    });
    return list.sort((a, b) => (sort === "score" ? b.score - a.score || b.firstSeen.localeCompare(a.firstSeen) : b.firstSeen.localeCompare(a.firstSeen) || b.posted.localeCompare(a.posted) || b.score - a.score));
  }, [state, query, sort, period, hideFlagged, showTracked]);

  const untracked = (state?.leads ?? []).filter((l) => !l.application);
  const newToday = untracked.filter((l) => l.firstSeen === today()).length;

  return (
    <div className="radar">
      <RunHeader state={state} onRun={run} />
      {err && <p className="status err">{err}</p>}

      <div className="bar">
        <div className="seg" role="group" aria-label="Radar section">
          {(
            [
              ["leads", `Leads (${untracked.length})`],
              ["skills", "Skill gap"],
              ["settings", "Settings"],
            ] as [Tab, string][]
          ).map(([t, label]) => (
            <button key={t} type="button" aria-pressed={tab === t} onClick={() => (setTab(t), keep("jh-radar-tab", t))}>
              {label}
            </button>
          ))}
        </div>
        {tab === "leads" && (
          <input className="search" type="search" placeholder="Search title, company, skill or location" aria-label="Search leads" value={query} onChange={(e) => setQuery(e.target.value)} />
        )}
      </div>

      {tab === "leads" && (
        <>
          <div className="filters">
            <label>
              Added{" "}
              <select value={period} onChange={(e) => (setPeriod(e.target.value as Period), keep("jh-radar-period", e.target.value))}>
                <option value="1">Today ({newToday})</option>
                <option value="7">Last 7 days</option>
                <option value="all">Any time</option>
              </select>
            </label>
            <label>
              Sort{" "}
              <select value={sort} onChange={(e) => (setSort(e.target.value as Sort), keep("jh-radar-sort", e.target.value))}>
                <option value="score">Best score</option>
                <option value="date">Newest</option>
              </select>
            </label>
            <label className="check">
              <input type="checkbox" checked={hideFlagged} onChange={(e) => (setHideFlagged(e.target.checked), keep("jh-radar-flagged", e.target.checked ? "1" : "0"))} />
              Hide flagged
            </label>
            <label className="check">
              <input type="checkbox" checked={showTracked} onChange={(e) => setShowTracked(e.target.checked)} />
              Include ones on the board
            </label>
          </div>
          {!state ? (
            <p className="lempty">Loading…</p>
          ) : leads.length === 0 ? (
            <p className="lempty empty-leads">
              {untracked.length === 0
                ? state.last
                  ? "No open leads. Every match is on the board, or nothing new scored high enough. The next check runs tomorrow, or press Check now."
                  : "The radar hasn't run yet. Press Check now, or wait: it checks the boards shortly after jobhunt starts each day."
                : "No leads match these filters."}
            </p>
          ) : (
            <div className="leads">
              {leads.map((l) => (
                <LeadCard
                  key={l.uid}
                  l={l}
                  busy={busy === l.uid}
                  conflict={conflict?.uid === l.uid ? conflict.res : null}
                  onAct={(stage) => (stage === "skipped" ? setSkipping(l) : act(l, stage))}
                  onForce={() => conflict && act(l, "saved", undefined, true)}
                  onOpenApp={onOpenApp}
                />
              ))}
            </div>
          )}
          <Credits state={state} />
        </>
      )}
      {tab === "skills" && <SkillGap />}
      {tab === "settings" && <RadarSettingsView onSaved={load} onStatus={onStatus} />}

      {skipping && (
        <SkipDialog
          title={`${skipping.title} at ${skipping.company}`}
          onCancel={() => setSkipping(null)}
          onSkip={(s) => {
            const l = skipping;
            setSkipping(null);
            act(l, "skipped", s);
          }}
        />
      )}
    </div>
  );
}

function RunHeader({ state, onRun }: { state: RadarState | null; onRun: () => void }) {
  const last = state?.last;
  const when = last ? new Date(last.finished || last.started) : null;
  const whenText = when ? (last!.date === today() ? `today at ${when.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}` : fmtDay(last!.date)) : "";
  return (
    <section className="runbox" aria-label="Radar status">
      <div className="runtop">
        <div>
          <b>{state?.running ? "Checking the boards…" : last ? `Last checked ${whenText}` : "Not run yet"}</b>
          {last && !state?.running && (
            <span className="hint">
              {" "}
              · {last.matches} new lead{last.matches === 1 ? "" : "s"} from {last.scanned} new listing{last.scanned === 1 ? "" : "s"}
              {last.hidden > 0 ? ` · ${last.hidden} already on the board` : ""}
            </span>
          )}
          {state && (
            <div className="hint">
              {state.autoRun ? "Runs once a day by itself while jobhunt is running." : "Daily runs are off (auto_run in Settings)."} Boards limit how often they can be checked, so a board checked recently is left out of a manual run.
            </div>
          )}
        </div>
        <button type="button" className="btn primary" disabled={!state || state.running} onClick={onRun}>
          {state?.running ? "Checking…" : "Check now"}
        </button>
      </div>
      {state?.configError && <p className="status err flush">Radar settings have a problem: {state.configError}</p>}
      {last?.error && <p className="status err flush">Last run failed: {last.error}</p>}
      {last && last.sources.length > 0 && (
        <div className="chips">
          {last.sources.map((s) => (
            <span key={s.name} className={"chip" + (s.error ? " bad" : s.skipped ? "" : " ft")} title={s.error || s.skipped || `${s.count} listings fetched`}>
              {s.name} {s.error ? "failed" : s.skipped ? "not checked" : s.count}
            </span>
          ))}
          {last.problems.map((p) => (
            <span key={p.name} className="chip warn" title={p.error}>
              {p.name} failed
            </span>
          ))}
        </div>
      )}
    </section>
  );
}

function LeadCard({ l, busy, conflict, onAct, onForce, onOpenApp }: {
  l: Lead;
  busy: boolean;
  conflict: CreateResult | null;
  onAct: (stage: "saved" | "applied" | "skipped") => void;
  onForce: () => void;
  onOpenApp: (id: string) => void;
}) {
  const link = webLink(l.url);
  const original = webLink(l.originalUrl);
  const isNew = l.firstSeen === today();
  return (
    <article className={"lead" + (l.application ? " tracked" : "")}>
      <div className="lead-h">
        <div className="score" title="Score: keyword matches minus penalties for restrictions">{l.score}</div>
        <div className="lead-t">
          <div className="co">
            {link ? (
              <a href={link} target="_blank" rel="noopener noreferrer">{l.title}</a>
            ) : (
              l.title
            )}
            {isNew && <span className="chip ft newchip">New</span>}
          </div>
          <div className="ro">{l.company}</div>
        </div>
      </div>
      <div className="meta wrapmeta">
        <span>{l.location}</span>
        {l.salary && <span>{l.salary}</span>}
        <span title={`Posted ${l.posted}`}>Posted {fmtDay(l.posted)}</span>
        <span title={`First listed ${l.firstSeen}`}>Added {l.firstSeen === today() ? "today" : fmtDay(l.firstSeen)}</span>
      </div>
      <div className="hint">
        via {link ? <a href={link} target="_blank" rel="noopener noreferrer">{l.sourceName}</a> : l.sourceName}
        {l.alsoOn.length > 0 && ` (also on ${l.alsoOn.join(", ")})`}
        {original && (
          <>
            {" · "}
            <a href={original} target="_blank" rel="noopener noreferrer">Original posting ({hostOf(original)})</a>
          </>
        )}
      </div>
      {l.flags.length > 0 && (
        <div className="chips">
          {l.flags.map((f) => <span key={f} className="chip warn">⚠ {f}</span>)}
        </div>
      )}
      {l.hits.length > 0 && <div className="hits">Matched: {l.hits.join(", ")}</div>}
      {l.snippet && <p className="snippet">{l.snippet}…</p>}
      {l.antispam && <div className="hint">Apply note: the post asks you to mention <b>{l.antispam}</b>.</div>}
      {conflict && (
        <div className="dupbox bad" role="alert">
          <b>{conflict.check.message}</b>
          <ul>
            {conflict.check.matches.map((m) => (
              <li key={m.id}>
                <button type="button" className="linkbtn" onClick={() => onOpenApp(m.id)}>{m.company}, {m.role}</button>
                <span className="hint"> {stageName(m.stage)}</span>
              </li>
            ))}
          </ul>
          {conflict.check.verdict === "possible_duplicate" && <button type="button" className="btn" onClick={onForce}>Save anyway</button>}
        </div>
      )}
      {l.application ? (
        <div className="lead-act">
          <span className={"chip" + (l.application.stage === "skipped" ? " bad" : " ft")}>
            {stageName(l.application.stage)}
            {l.application.stage === "skipped" && l.application.skipReason ? `: ${skipLabel(l.application.skipReason)}` : ""}
          </span>
          <button type="button" className="btn" onClick={() => onOpenApp(l.application!.id)}>Open on board</button>
        </div>
      ) : (
        <div className="lead-act">
          <button type="button" className="btn" disabled={busy} onClick={() => onAct("saved")}>Save to board</button>
          <button type="button" className="btn" disabled={busy} onClick={() => onAct("applied")}>Applied</button>
          <button type="button" className="btn ghost" disabled={busy} onClick={() => onAct("skipped")}>Skip…</button>
        </div>
      )}
    </article>
  );
}

function hostOf(u: string) {
  try {
    return new URL(u).hostname.replace(/^www\./, "");
  } catch {
    return u;
  }
}

// Credits: each board's terms ask to be named and linked.
function Credits({ state }: { state: RadarState | null }) {
  const sources = state?.last?.sources ?? [];
  if (!sources.length) return null;
  return (
    <p className="hint credits">
      Listings from{" "}
      {sources.map((s, i) => (
        <span key={s.name}>
          {i > 0 && (i === sources.length - 1 ? " and " : ", ")}
          <a href={webLink(s.home)} target="_blank" rel="noopener noreferrer">{s.name}</a>
        </span>
      ))}
      , through their public feeds. Each lead links to its own board. For your own job search only; don't republish them.
    </p>
  );
}

const STATUS_ICON = { have: "✅ have", learning: "📘 learning", gap: "❌ gap" } as const;

function SkillGap() {
  const [r, setR] = useState<SkillReport | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api.radarSkills().then(setR).catch((e) => setErr(e instanceof Error ? e.message : "Couldn't load the report."));
  }, []);
  if (err) return <p className="status err">{err}</p>;
  if (!r) return <p className="lempty">Loading…</p>;
  return (
    <div className="skillgap">
      <p className="sub">
        Based on <b>{r.listings}</b> relevant listings from the last {r.windowDays} days, across every board. A listing counts as relevant when its title looks like a role you'd consider (relevant_title in the skills settings).
      </p>
      {!r.enough ? (
        <p className="lempty">Not enough data yet ({r.listings} listings, need {r.minJobs}). The report fills in as the daily runs collect more.</p>
      ) : (
        <>
          <section className="stats">
            <div className="stat"><div className="k">Coverage</div><div className="v">{r.coverage}%</div><div className="h">of a typical listing's skills you have</div></div>
            <div className="stat"><div className="k">Listings</div><div className="v">{r.listings}</div><div className="h">{r.asked} named a tracked skill</div></div>
            <div className="stat alert"><div className="k">Top gap</div><div className="v sm">{r.gaps[0]?.skill ?? "—"}</div><div className="h">{r.gaps[0] ? `${r.gaps[0].share}% of listings` : "none"}</div></div>
          </section>
          <SkillTable title="Top gaps to close" rows={r.gaps} showArea />
          <SkillTable title="All tracked skills" rows={r.skills} showStatus />
          <section className="lstats">
            <h2>By area</h2>
            <div className="tbl-wrap">
              <table>
                <thead><tr><th>Area</th><th className="num">Listings mentioning it</th><th>Your status</th></tr></thead>
                <tbody>
                  {r.areas.map((a) => (
                    <tr key={a.area}><td>{a.area}</td><td className="num">{a.share}%</td><td>have {a.have} of {a.seen} skills seen</td></tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

function SkillTable({ title, rows, showArea, showStatus }: { title: string; rows: SkillReport["skills"]; showArea?: boolean; showStatus?: boolean }) {
  return (
    <section className="lstats">
      <h2>{title}</h2>
      {rows.length === 0 ? (
        <p className="lempty">None.</p>
      ) : (
        <div className="tbl-wrap">
          <table>
            <thead>
              <tr>
                <th>Skill</th>
                {showArea && <th>Area</th>}
                {showStatus && <th>Status</th>}
                <th className="num">Share</th>
                <th className="num">Listings</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <tr key={s.skill}>
                  <td>{s.skill}</td>
                  {showArea && <td>{s.area}</td>}
                  {showStatus && <td>{STATUS_ICON[s.status]}</td>}
                  <td className="num"><span className="rbar" style={{ width: Math.max(2, s.share) * 0.8 }} />{s.share}%</td>
                  <td className="num">{s.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function RadarSettingsView({ onSaved, onStatus }: { onSaved: () => void; onStatus: (msg: string, err?: boolean) => void }) {
  const [s, setS] = useState<RadarSettings | null>(null);
  const [config, setConfig] = useState("");
  const [skills, setSkills] = useState("");
  const [msg, setMsg] = useState({ text: "", err: false });
  const [ref, setRef] = useState("");
  const [name, setName] = useState("");
  const [adding, setAdding] = useState(false);

  const apply = (x: RadarSettings) => {
    setS(x);
    setConfig(x.config);
    setSkills(x.skills);
  };
  useEffect(() => {
    api.radarSettings().then(apply).catch((e) => setMsg({ text: e instanceof Error ? e.message : "Couldn't load settings.", err: true }));
  }, []);

  const save = async (body: { config?: string; skills?: string }, done: string) => {
    try {
      apply(await api.saveRadarSettings(body));
      setMsg({ text: done, err: false });
      onSaved();
    } catch (e) {
      setMsg({ text: e instanceof Error ? e.message : "Couldn't save.", err: true });
    }
  };

  const addCompany = async (e: FormEvent) => {
    e.preventDefault();
    setAdding(true);
    try {
      const r = await api.radarAddCompany(ref, name);
      onStatus(`Added ${r.name} (${r.ats}:${r.token}): ${r.jobs} open job${r.jobs === 1 ? "" : "s"}, ${r.remote} remote.`);
      setRef("");
      setName("");
      apply(await api.radarSettings());
      setMsg({ text: "", err: false });
    } catch (err) {
      setMsg({ text: err instanceof Error ? err.message : "Couldn't add that board.", err: true });
    } finally {
      setAdding(false);
    }
  };

  if (!s) return msg.text ? <p className="status err">{msg.text}</p> : <p className="lempty">Loading…</p>;
  return (
    <div className="rsettings">
      <form className="lstats" onSubmit={addCompany}>
        <h2>Target companies</h2>
        <p className="hint flush">
          Paste a company's Greenhouse, Lever or Ashby job-board link (or ats:token). The radar checks the board once, then reads it daily and keeps the remote roles.
        </p>
        <div className="row">
          <input aria-label="Board link" required placeholder="https://jobs.lever.co/acme" value={ref} onChange={(e) => setRef(e.target.value)} />
          <input aria-label="Company name (optional)" placeholder="Name (optional)" value={name} onChange={(e) => setName(e.target.value)} />
          <button type="submit" className="btn primary" disabled={adding}>{adding ? "Checking…" : "Add"}</button>
        </div>
      </form>

      {msg.text && <p className={"status" + (msg.err ? " err" : "")} aria-live="polite">{msg.text}</p>}

      <JsonEditor
        id="rcfg"
        title="Scoring and boards (config.json)"
        hint="Sources run in the order listed; the first board to list a job wins. Patterns are case-insensitive regular expressions, written as in Job Radar."
        value={config}
        onChange={setConfig}
        error={s.configError}
        custom={s.configIsCustom}
        onSave={() => save({ config }, "Scoring settings saved. They apply from the next run.")}
        onReset={() => save({ config: "" }, "Scoring settings back to the defaults.")}
      />
      <JsonEditor
        id="rskills"
        title="Skills and profile (skills.json)"
        hint="profile.have and profile.learning are yours; every other skill counts as a gap."
        value={skills}
        onChange={setSkills}
        error={s.skillsError}
        custom={s.skillsIsCustom}
        onSave={() => save({ skills }, "Skills saved.")}
        onReset={() => save({ skills: "" }, "Skills back to the defaults.")}
      />
    </div>
  );
}

function JsonEditor(p: { id: string; title: string; hint: string; value: string; onChange: (v: string) => void; error?: string; custom: boolean; onSave: () => void; onReset: () => void }) {
  return (
    <section className="lstats">
      <div className="lhead">
        <h2 id={p.id + "-t"}>{p.title}</h2>
        <span className="hint">{p.custom ? "Customised" : "Defaults"}</span>
      </div>
      <p className="hint flush">{p.hint}</p>
      <textarea className="json" aria-labelledby={p.id + "-t"} spellCheck={false} value={p.value} onChange={(e) => p.onChange(e.target.value)} />
      {p.error && <p className="status err flush">{p.error}</p>}
      <div className="actions">
        {p.custom ? <button type="button" className="btn ghost danger" onClick={p.onReset}>Reset to defaults</button> : <span />}
        <button type="button" className="btn primary" onClick={p.onSave}>Save</button>
      </div>
    </section>
  );
}
