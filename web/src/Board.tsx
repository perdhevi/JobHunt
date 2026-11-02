import { useDraggable, useDroppable } from "@dnd-kit/core";
import { Application, daysSince, Doc, fmtDay, STAGES, today } from "./api";

export function docName(docs: Doc[], id: string) {
  if (!id) return "";
  return docs.find((d) => d.id === id)?.name ?? "(deleted version)";
}

export function Board({ apps, docs, onOpen }: { apps: Application[]; docs: Doc[]; onOpen: (id: string) => void }) {
  return (
    <div className="board-scroll">
      <div className="board">
        {STAGES.map((s) => {
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
    <button
      ref={setNodeRef}
      type="button"
      className={"card" + (isDragging ? " dragging" : "")}
      onClick={() => onOpen(a.id)}
      {...listeners}
      {...attributes}
      role="button"
    >
      <CardBody a={a} docs={docs} />
    </button>
  );
}

export function CardBody({ a, docs }: { a: Application; docs: Doc[] }) {
  const late = !!a.nextDate && a.nextDate <= today() && a.stage !== "closed";
  const d = daysSince(a.applied);
  const letter = docName(docs, a.letterId);
  const resume = docName(docs, a.resumeId);
  return (
    <>
      <div>
        <div className="co">{a.company}</div>
        <div className="ro">{a.role}</div>
      </div>
      <div className="chips">
        <span className={"chip " + (a.track === "fulltime" ? "ft" : "up")}>{a.track === "fulltime" ? "Full-time" : "Contract"}</span>
        {a.source && <span className="chip">{a.source}</span>}
        {a.outcome && <span className={"chip " + (a.outcome === "Accepted" ? "ft" : "bad")}>{a.outcome}</span>}
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
      <div className="meta">
        <span>{a.applied ? "Applied " + fmtDay(a.applied) : "Not applied yet"}</span>
        <span>{d != null && a.stage !== "closed" ? d + "d" : ""}</span>
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
