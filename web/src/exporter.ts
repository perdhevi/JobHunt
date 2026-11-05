import { Application, Doc, STAGES, today } from "./api";

export type Format = "md" | "csv" | "json";

export interface ExportOptions {
  safe: boolean; // leave out contacts, pay, notes, letter text and links
  anon: boolean; // replace company names
}

const stageName = (id: string) => STAGES.find((s) => s.id === id)?.name ?? id;
const docName = (docs: Doc[], id: string) => (id ? docs.find((d) => d.id === id)?.name ?? "(deleted version)" : "");
const docFile = (docs: Doc[], id: string) => docs.find((d) => d.id === id)?.file?.name ?? "";

function rows(apps: Application[], docs: Doc[], o: ExportOptions) {
  const names = new Map<string, string>();
  const company = (a: Application) => {
    if (!o.anon) return a.company;
    const k = a.company.toLowerCase();
    if (!names.has(k)) {
      let n = names.size;
      let s = "";
      do {
        s = String.fromCharCode(65 + (n % 26)) + s;
        n = Math.floor(n / 26) - 1;
      } while (n >= 0);
      names.set(k, "Company " + s);
    }
    return names.get(k)!;
  };
  const list = apps.slice().sort((a, b) => (b.applied || "").localeCompare(a.applied || ""));
  const out = list.map((a) => {
    const r: Record<string, string> = {
      ref: a.ref,
      company: company(a),
      role: a.role,
      track: a.track === "fulltime" ? "Full-time" : "Contract",
      source: a.source,
      stage: stageName(a.stage),
      outcome: a.outcome,
      applied: a.applied,
      cover_letter: docName(docs, a.letterId),
      cover_file: a.coverFile?.name || docFile(docs, a.letterId),
      resume: docName(docs, a.resumeId),
      resume_file: a.resumeFile?.name || docFile(docs, a.resumeId),
    };
    if (!o.safe) Object.assign(r, { comp: a.comp, contact: a.contact, next_step: a.nextStep, next_date: a.nextDate, url: a.url, notes: a.notes, letter_as_sent: a.letterText });
    return r;
  });
  return { list, out };
}

const HEAD: Record<string, string> = {
  ref: "Ref", company: "Company", role: "Role", track: "Track", source: "Source", stage: "Stage", outcome: "Outcome", applied: "Applied",
  cover_letter: "Cover letter", cover_file: "Cover letter file", resume: "Resume", resume_file: "Resume file", comp: "Salary / rate",
  contact: "Contact", next_step: "Next step", next_date: "Due", url: "Link", notes: "Notes", letter_as_sent: "Letter as sent",
};

function toMarkdown(apps: Application[], docs: Doc[], o: ExportOptions) {
  const { list, out } = rows(apps, docs, o);
  const cell = (s: unknown) => String(s ?? "").replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
  const pct = (a: number, b: number) => (b ? Math.round((a / b) * 100) + "%" : "—");
  const sent = list.filter((a) => a.sent);
  const rep = sent.filter((a) => a.replied);
  const offers = list.filter((a) => a.stage === "offer" || a.outcome === "Accepted" || a.outcome === "Declined offer").length;
  let md = `# Job Hunt Pipeline\n\n_Exported ${today()}_\n\n`;
  md += `| Sent | Replies | Response rate | Interviewing | Offers |\n|---:|---:|---:|---:|---:|\n| ${sent.length} | ${rep.length} | ${pct(rep.length, sent.length)} | ${list.filter((a) => a.stage === "interview").length} | ${offers} |\n\n`;
  md += `## By stage\n\n| Stage | Count |\n|---|---:|\n` + STAGES.map((s) => `| ${s.name} | ${list.filter((a) => a.stage === s.id).length} |`).join("\n") + "\n\n";
  if (docs.length) {
    md += `## Document versions\n\n| Type | Version | Sent | Replies | Reply rate |\n|---|---|---:|---:|---:|\n`;
    md += docs
      .map((d) => {
        const s = sent.filter((a) => (d.kind === "resume" ? a.resumeId : a.letterId) === d.id);
        const r = s.filter((a) => a.replied);
        return `| ${d.kind === "resume" ? "Resume" : "Cover letter"} | ${cell(d.name)} | ${s.length} | ${r.length} | ${pct(r.length, s.length)} |`;
      })
      .join("\n");
    md += "\n\n";
  }
  if (out.length) {
    const cols = Object.keys(out[0]);
    md += `## Applications\n\n| ${cols.map((c) => HEAD[c]).join(" | ")} |\n|${cols.map(() => "---").join("|")}|\n`;
    md += out.map((r) => "| " + cols.map((c) => cell(r[c])).join(" | ") + " |").join("\n") + "\n";
  }
  return md;
}

function toCSV(apps: Application[], docs: Doc[], o: ExportOptions) {
  const { out } = rows(apps, docs, o);
  if (!out.length) return "ref,company,role,track,source,stage,outcome,applied,cover_letter,cover_file,resume,resume_file\n";
  const cols = Object.keys(out[0]);
  // A cell starting with = + - @ or a tab is a formula to Excel and Sheets, and
  // company names and notes come from job posts an agent read. The quote makes
  // it text.
  const inert = (s: string) => (/^[=+\-@\t\r]/.test(s) ? "'" + s : s);
  const q = (raw: string) => {
    const s = inert(raw ?? "");
    return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  return "﻿" + [cols.join(","), ...out.map((r) => cols.map((c) => q(r[c])).join(","))].join("\r\n");
}

// JSON is the backup format: "jobhunt import" reads it back unless share-safe stripped fields.
function toJSON(apps: Application[], docs: Doc[], o: ExportOptions) {
  const { list, out } = rows(apps, docs, o);
  const strip = (a: Application, i: number) => {
    const c: Partial<Application> = { ...a };
    delete c.due;
    delete c.replied;
    delete c.sent;
    delete c.version;
    if (o.safe) for (const k of ["comp", "contact", "nextStep", "nextDate", "url", "notes", "letterText"] as const) delete c[k];
    if (o.anon) c.company = out[i].company;
    return c;
  };
  return JSON.stringify({ exported: today(), applications: list.map(strip), letters: o.safe ? docs.map(({ id, kind, name, use, file }) => ({ id, kind, name, use, file })) : docs }, null, 2);
}

export function buildExport(fmt: Format, apps: Application[], docs: Doc[], o: ExportOptions) {
  return fmt === "md" ? toMarkdown(apps, docs, o) : fmt === "csv" ? toCSV(apps, docs, o) : toJSON(apps, docs, o);
}
