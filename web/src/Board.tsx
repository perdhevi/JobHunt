import { useDraggable, useDroppable } from "@dnd-kit/core";
import { Application, daysSince, Doc, fmtDay, skipLabel, STAGES, today } from "./api";

// webLink returns the job link only if it's http(s). The server refuses other
// schemes, and this also covers records saved before it did: React 18 still
// renders a javascript: href.
export function webLink(url: string) {
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : "";
  } catch {
    return "";
  }
}

export function docName(docs: Doc[], id: string) {
  if (!id) return "";
  return docs.find((d) => d.id === id)?.name ?? "(deleted version)";
}

export function Board({ apps, docs, onOpen, showSkipped }: { apps: Application[]; docs: Doc[]; onOpen: (id: string) => void; showSkipped: boolean }) {
  const stages = STAGES.filter((s) => showSkipped || s.id !== "skipped");
  return (
    <div className="board-scroll">
      <div className="board" style={{ gridTemplateColumns: `repeat(${stages.length}, minmax(220px, 1fr))`, minWidth: stages.length * 230 }}>
        {stages.map((s) => {
          const items = apps
            .filter((a) => a.stage === s.id)
            .sort((x, y) => (x.nextDate || "9999").localeCompare(y.nextDate || "9999") || y.updatedAt - x.updatedAt);
          return (
            <Column key={s.id} id={s.id} name={s.name} count={items.length}>
              {items.length ? items.map((a) => <Card key={a.id} a={a} docs={docs} onOpen={onOpen} />) : <div className="empty">Nothing here</div>}
            </Column>
          );
        })}
      </div>
    </div>
  );
}

function Column({ id, name, count, children }: { id: string; name: string; count: number; children: React.ReactNode }) {
  const { setNodeRef, isOver } = useDroppable({ id });
  return (
    <div ref={setNodeRef} className={"col" + (isOver ? " over" : "")}>
      <div className="col-h">
        <b>{name}</b>
        <span className="n">{count}</span>
      </div>
      {children}
    </div>
  );
}

function Card({ a, docs, onOpen }: { a: Application; docs: Doc[]; onOpen: (id: string) => void }) {
  const { setNodeRef, listeners, attributes, isDragging } = useDraggable({ id: a.id });
  return (
    <div
      ref={setNodeRef}
      className={"card" + (isDragging ? " dragging" : "")}
      onClick={() => onOpen(a.id)}
      onKeyDown={(e) => {
        if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
          e.preventDefault();
          onOpen(a.id);
        }
      }}
      {...listeners}
      {...attributes}
      role="button"
    >
      <CardBody a={a} docs={docs} />
    </div>
  );
}

export function CardBody({ a, docs }: { a: Application; docs: Doc[] }) {
  const late = !!a.nextDate && a.nextDate <= today() && a.stage !== "closed";
  const d = daysSince(a.applied);
  const letter = docName(docs, a.letterId);
  const resume = docName(docs, a.resumeId);
  const link = webLink(a.url);
  return (
    <>
      <div className="card-h">
        <div>
          <div className="co">{a.company}</div>
          <div className="ro">{a.role}</div>
        </div>
        {link && (
          <a
            className="post-link"
            href={link}
            target="_blank"
            rel="noopener noreferrer"
            title="Open job post"
            aria-label={`Open job post for ${a.company}`}
            onClick={(e) => e.stopPropagation()}
            onPointerDown={(e) => e.stopPropagation()}
          >
            ↗
          </a>
        )}
      </div>
      <div className="chips">
        <span className={"chip " + (a.track === "fulltime" ? "ft" : "up")}>{a.track === "fulltime" ? "Full-time" : "Contract"}</span>
        {a.source && <span className="chip">{a.source}</span>}
        {a.outcome && <span className={"chip " + (a.outcome === "Accepted" ? "ft" : "bad")}>{a.outcome}</span>}
        {a.stage === "skipped" && <span className="chip bad">{skipLabel(a.skipReason)}</span>}
        {a.due && !late && <span className="chip warn">Follow up</span>}
        {(a.letterId || a.coverFile || a.letterText) && (
          <span className="chip lt" title={a.coverFile ? `Cover letter sent: ${a.coverFile.name}` : "Cover letter sent"}>
            ✉ {letter || "Cover letter"}{a.coverFile ? " · 📎" : ""}{a.letterText ? " · tailored" : ""}
          </span>
        )}
        {(a.resumeId || a.resumeFile) && (
          <span className="chip lt" title={a.resumeFile ? `Resume sent: ${a.resumeFile.name}` : "Resume sent"}>
            📄 {resume || "Resume"}{a.resumeFile ? " · 📎" : ""}
          </span>
        )}
      </div>
      {a.stage === "skipped" && a.skipNote && <div className="skipnote">{a.skipNote}</div>}
      <div className="meta">
        <span>{a.stage === "skipped" ? "Skipped " + fmtDay(a.skipped) : a.applied ? "Applied " + fmtDay(a.applied) : "Not applied yet"}</span>
        <span>{d != null && a.stage !== "closed" && a.stage !== "skipped" ? d + "d" : ""}</span>
      </div>
      <div className="ref">+{a.ref}</div>
      {(a.nextStep || a.nextDate) && (
        <div className={"next" + (late ? " late" : "")}>
          {a.nextStep || "Next step"}
          {a.nextDate && <> · <span className="d">{fmtDay(a.nextDate)}</span></>}
        </div>
      )}
      {a.comp && <div className="meta"><span>{a.comp}</span></div>}
    </>
  );
}
