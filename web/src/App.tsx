import { useCallback, useEffect, useMemo, useState } from "react";
import { DndContext, DragEndEvent, DragOverlay, DragStartEvent, PointerSensor, useSensor, useSensors } from "@dnd-kit/core";
import { api, Application, Doc, OUTCOMES, Settings, STAGES } from "./api";
import { Board, CardBody } from "./Board";
import { Editor } from "./Editor";
import { Documents } from "./Documents";
import { ExportPanel } from "./ExportPanel";
import { SettingsPanel } from "./SettingsPanel";

const POLL_MS = 5000; // MCP clients write from other processes

type Track = "all" | "fulltime" | "contract";

function loadTrack(): Track {
  try {
    const t = localStorage.getItem("jh-track");
    if (t === "fulltime" || t === "contract") return t;
  } catch {
    /* storage may be blocked */
  }
  return "all";
}

export default function App() {
  const [apps, setApps] = useState<Application[]>([]);
  const [docs, setDocs] = useState<Doc[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [remote, setRemote] = useState(false);
  const [track, setTrack] = useState<Track>(loadTrack);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState({ msg: "Connecting…", err: false });
  const [editor, setEditor] = useState<{ id: string | null } | null>(null);
  const [panel, setPanel] = useState<"docs" | "export" | "settings" | null>(null);
  const [dragging, setDragging] = useState<Application | null>(null);
  const [closing, setClosing] = useState<Application | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [a, d] = await Promise.all([api.listApps(), api.listDocs()]);
      setApps(a);
      setDocs(d);
      setStatus({ msg: a.length ? `${a.length} application${a.length > 1 ? "s" : ""} tracked` : "No applications yet. Add your first one above.", err: false });
    } catch {
      setStatus({ msg: "Can't reach the server. Is jobhunt running?", err: true });
    }
  }, []);

  useEffect(() => {
    refresh();
    api.settings().then(setSettings).catch(() => {});
    api.session().then((s) => setRemote(s.remote)).catch(() => {});
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const ref = q.match(/\+([0-9a-z]{4,12})@/)?.[1];
    if (ref) return apps.filter((a) => a.ref === ref);
    return apps.filter(
      (a) =>
        (track === "all" || a.track === track) &&
        (!q || [a.company, a.role, a.notes, a.contact, a.source, a.ref].join(" ").toLowerCase().includes(q)),
    );
  }, [apps, track, query]);

  const stats = useMemo(() => {
    const sent = visible.filter((a) => a.sent);
    const replied = sent.filter((a) => a.replied);
    return {
      inFlight: sent.filter((a) => a.stage !== "closed").length,
      interviews: visible.filter((a) => a.stage === "interview").length,
      sent: sent.length,
      replied: replied.length,
      rate: sent.length ? Math.round((replied.length / sent.length) * 100) + "%" : "—",
      due: visible.filter((a) => a.due),
    };
  }, [visible]);

  const move = async (a: Application, stage: string, outcome = "") => {
    setApps((list) => list.map((x) => (x.id === a.id ? { ...x, stage, outcome } : x)));
    try {
      await api.moveApp(a.id, stage, outcome);
      setStatus({ msg: `${a.company} moved to ${STAGES.find((s) => s.id === stage)?.name}.`, err: false });
    } catch (e) {
      setStatus({ msg: e instanceof Error ? e.message : "Couldn't move that card.", err: true });
    }
    refresh();
  };

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }));
  const onDragStart = (e: DragStartEvent) => setDragging(apps.find((a) => a.id === e.active.id) ?? null);
  const onDragEnd = (e: DragEndEvent) => {
    const a = dragging;
    setDragging(null);
    const stage = e.over?.id as string | undefined;
    if (!a || !stage || stage === a.stage) return;
    if (stage === "closed") setClosing(a);
    else move(a, stage);
  };

  const selected = editor?.id ? apps.find((a) => a.id === editor.id) ?? null : null;

  return (
    <div className="wrap">
      <header className="top">
        <div>
          <h1>Job Hunt Pipeline</h1>
          <p className="sub">Every application, from saved lead to signed offer. Drag cards between stages. Agents connected over MCP show up here within seconds.</p>
        </div>
        <div className="hdr-actions">
          <button className="btn" onClick={() => setPanel("settings")}>Settings</button>
          <button className="btn" onClick={() => setPanel("export")}>Export</button>
          <button className="btn" onClick={() => setPanel("docs")}>Documents</button>
          <button className="btn primary" onClick={() => setEditor({ id: null })}>+ Add application</button>
          {remote && (
            <form method="post" action="/logout" className="inline">
              <button className="btn ghost" type="submit">Sign out</button>
            </form>
          )}
        </div>
      </header>

      <section className="stats" aria-label="Summary">
        <Stat k="In flight" v={stats.inFlight} h="applied, not yet closed" />
        <Stat k="Interviews" v={stats.interviews} h="active loops" />
        <Stat k="Response rate" v={stats.rate} h={`${stats.replied} of ${stats.sent} sent got a reply`} />
        <Stat k="Follow-ups due" v={stats.due.length} h={stats.due.length ? "see list below" : "nothing waiting"} alert={stats.due.length > 0} />
      </section>

      {stats.due.length > 0 && (
        <section className="due">
          <h2>Needs attention</h2>
          <ul>
            {stats.due.map((a) => (
              <li key={a.id}>
                <button type="button" onClick={() => setEditor({ id: a.id })}>{a.company}</button>
                <span className="why">{a.due}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="bar">
        <div className="seg" role="group" aria-label="Track filter">
          {(["all", "fulltime", "contract"] as Track[]).map((t) => (
            <button
              key={t}
              type="button"
              aria-pressed={track === t}
              onClick={() => {
                setTrack(t);
                try {
                  localStorage.setItem("jh-track", t);
                } catch {
                  /* storage may be blocked */
                }
              }}
            >
              {t === "all" ? "All" : t === "fulltime" ? "Full-time" : "Contract / Upwork"}
            </button>
          ))}
        </div>
        <input
          className="search"
          type="search"
          placeholder="Search company, role, notes, or paste a +ref address"
          aria-label="Search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      <DndContext sensors={sensors} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragCancel={() => setDragging(null)}>
        <Board apps={visible} docs={docs} onOpen={(id) => setEditor({ id })} />
        <DragOverlay dropAnimation={null}>{dragging && <div className="card overlay"><CardBody a={dragging} docs={docs} /></div>}</DragOverlay>
      </DndContext>

      <p className={"status" + (status.err ? " err" : "")} role="status">{status.msg}</p>

      {editor && (
        <Editor
          key={editor.id ?? "new"}
          app={selected}
          docs={docs}
          email={settings?.email ?? ""}
          onClose={() => setEditor(null)}
          onOpenOther={(id) => setEditor({ id })}
          onSaved={(msg) => {
            setEditor(null);
            setStatus({ msg, err: false });
            refresh();
          }}
          onChanged={refresh}
        />
      )}
      {panel === "docs" && <Documents apps={apps} docs={docs} onClose={() => setPanel(null)} onChanged={refresh} />}
      {panel === "export" && <ExportPanel apps={apps} docs={docs} onClose={() => setPanel(null)} />}
      {panel === "settings" && settings && <SettingsPanel settings={settings} onClose={() => setPanel(null)} onSaved={setSettings} />}

      {closing && (
        <div className="scrim center" onClick={(e) => e.target === e.currentTarget && setClosing(null)}>
          <div className="dialog" role="dialog" aria-labelledby="closeTitle">
            <h2 id="closeTitle">Close {closing.company}?</h2>
            <p className="sub">How did it end?</p>
            <div className="outcomes">
              {OUTCOMES.map((o) => (
                <button
                  key={o}
                  type="button"
                  className="btn"
                  onClick={() => {
                    move(closing, "closed", o);
                    setClosing(null);
                  }}
                >
                  {o}
                </button>
              ))}
            </div>
            <div className="actions end"><button type="button" className="btn ghost" onClick={() => setClosing(null)}>Cancel</button></div>
          </div>
        </div>
      )}
    </div>
  );
}

function Stat({ k, v, h, alert }: { k: string; v: number | string; h: string; alert?: boolean }) {
  return (
    <div className={"stat" + (alert ? " alert" : "")}>
      <div className="k">{k}</div>
      <div className="v">{v}</div>
      <div className="h">{h}</div>
    </div>
  );
}
