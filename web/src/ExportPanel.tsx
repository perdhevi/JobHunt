import { useMemo, useState } from "react";
import { Application, Doc, today } from "./api";
import { buildExport, Format } from "./exporter";

const HINT: Record<Format, string> = {
  md: "Renders as tables on GitHub. Paste it into a README or save it as a .md file.",
  csv: "Opens in Excel or Google Sheets. One row per application.",
  json: "Everything, including document versions, so it can be restored with jobhunt import. Keep this one private.",
};
const TYPES: Record<Format, string> = { md: "text/markdown", csv: "text/csv", json: "application/json" };

export function ExportPanel({ apps, docs, onClose }: { apps: Application[]; docs: Doc[]; onClose: () => void }) {
  const [fmt, setFmt] = useState<Format>("md");
  const [safe, setSafe] = useState(true);
  const [anon, setAnon] = useState(false);
  const [msg, setMsg] = useState("");
  const text = useMemo(() => buildExport(fmt, apps, docs, { safe, anon }), [fmt, apps, docs, safe, anon]);

  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: TYPES[fmt] }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `job-hunt-pipeline-${today()}.${fmt}`;
    a.click();
    URL.revokeObjectURL(url);
    setMsg("Saved " + a.download + ".");
  };

  return (
    <div className="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="panel" role="dialog" aria-labelledby="xTitle">
        <div className="lhead"><h2 id="xTitle">Export</h2><button type="button" className="btn" onClick={onClose}>Done</button></div>
        <div className="f">
          <label>Format</label>
          <div className="seg" role="group" aria-label="Format">
            {(["md", "csv", "json"] as Format[]).map((f) => (
              <button
                key={f}
                type="button"
                aria-pressed={fmt === f}
                onClick={() => {
                  setFmt(f);
                  setSafe(f !== "json");
                }}
              >
                {f === "md" ? "Markdown" : f === "csv" ? "CSV" : "JSON backup"}
              </button>
            ))}
          </div>
          <span className="hint">{HINT[fmt]}</span>
        </div>
        <label className="check"><input type="checkbox" checked={safe} onChange={(e) => setSafe(e.target.checked)} /> Share-safe: leave out contacts, salary/rate, notes, letter text and job links</label>
        <label className="check"><input type="checkbox" checked={anon} onChange={(e) => setAnon(e.target.checked)} /> Hide company names (show as "Company A", "Company B"…)</label>
        <div className="f"><label htmlFor="x-prev">Preview</label><textarea id="x-prev" className="preview" readOnly value={text.replace(/^﻿/, "")} /></div>
        {msg && <p className="status" aria-live="polite">{msg}</p>}
        <div className="actions">
          <span />
          <div className="r">
            <button
              type="button"
              className="btn"
              onClick={async () => {
                await navigator.clipboard.writeText(text.replace(/^﻿/, ""));
                setMsg("Copied.");
              }}
            >
              Copy
            </button>
            <button type="button" className="btn primary" onClick={download}>Download file</button>
          </div>
        </div>
      </div>
    </div>
  );
}
