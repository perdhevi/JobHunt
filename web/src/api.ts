export const STAGES = [
  { id: "saved", name: "Saved" },
  { id: "applied", name: "Applied" },
  { id: "screening", name: "Screening" },
  { id: "interview", name: "Interviewing" },
  { id: "offer", name: "Offer" },
  { id: "closed", name: "Closed" },
  { id: "skipped", name: "Skipped" },
] as const;

// Why a job was skipped. Mirrors store.SkipReasons; "other" needs details.
export const SKIP_REASONS = [
  { key: "rate", label: "Rate too low" },
  { key: "location", label: "Location not applicable" },
  { key: "timezone", label: "Time zone doesn't fit" },
  { key: "seniority", label: "Seniority mismatch" },
  { key: "language", label: "Language required" },
  { key: "not_interested", label: "Not interested" },
  { key: "other", label: "Other" },
] as const;

export const skipLabel = (key: string) => SKIP_REASONS.find((r) => r.key === key)?.label ?? key;

export const SOURCES = ["LinkedIn", "Upwork", "We Work Remotely", "Company site", "Referral", "Recruiter inbound", "X / Twitter", "Other"];
export const OUTCOMES = ["Rejected", "No response", "Role closed", "Withdrew", "Declined offer", "Accepted"];

export interface FileMeta {
  id: string;
  name: string;
  size: number;
  type: string;
}

export interface Application {
  id: string;
  ref: string;
  company: string;
  role: string;
  track: "fulltime" | "contract";
  source: string;
  stage: string;
  outcome: string;
  applied: string;
  comp: string;
  nextStep: string;
  nextDate: string;
  contact: string;
  url: string;
  notes: string;
  letterId: string;
  letterText: string;
  resumeId: string;
  coverFile: FileMeta | null;
  resumeFile: FileMeta | null;
  skipReason: string;
  skipNote: string;
  skipped: string;
  version: number;
  createdAt: number;
  updatedAt: number;
  due: string;
  replied: boolean;
  sent: boolean;
}

export interface Doc {
  id: string;
  kind: "cover" | "resume";
  name: string;
  use: string;
  body: string;
  file: FileMeta | null;
  createdAt?: number;
  updatedAt?: number;
}

export interface Match {
  id: string;
  ref: string;
  company: string;
  role: string;
  stage: string;
  outcome?: string;
  applied?: string;
  sent: boolean;
  matchedOn: string;
  skipReason?: string;
  skipNote?: string;
  skipped?: string;
}

export interface CheckResult {
  verdict: "new" | "possible_duplicate" | "already_tracked" | "already_sent" | "skipped";
  safeToSend: boolean;
  message: string;
  matches: Match[];
  otherRoles: Match[];
}

export interface CreateResult {
  created: boolean;
  application?: Application;
  check: CheckResult;
}

export interface Settings {
  email: string;
  dbPath: string;
  activePath: string;
  configPath: string;
  docker: boolean;
  dbPathFixed: boolean; // JOBHUNT_DB picks the database, as on a VPS
}

export interface Skip {
  reason: string;
  note: string;
}

export interface LeadApp {
  id: string;
  ref: string;
  stage: string;
  applied?: string;
  skipReason?: string;
  skipNote?: string;
  skipped?: string;
}

export interface Lead {
  uid: string;
  source: string;
  sourceName: string;
  title: string;
  company: string;
  url: string;
  originalUrl: string;
  location: string;
  salary: string;
  posted: string;
  score: number;
  hits: string[];
  flags: string[];
  alsoOn: string[];
  altUids: string[];
  snippet: string;
  antispam: string;
  firstSeen: string;
  application?: LeadApp;
  skills: { skill: string; proof: Proof }[];
}

// What you can show for a skill: "have" with proof, "have" without, learning, or a gap.
export type Proof = "proven" | "claimed" | "learning" | "gap";

export const EVIDENCE_KINDS = [
  { key: "project", label: "Running project" },
  { key: "repo", label: "Code repository" },
  { key: "certificate", label: "Certificate" },
  { key: "course", label: "Course" },
  { key: "writeup", label: "Write-up" },
  { key: "work", label: "Work example" },
] as const;

export const kindLabel = (k: string) => EVIDENCE_KINDS.find((x) => x.key === k)?.label ?? k;

export interface Evidence {
  id: string;
  skill: string;
  kind: string;
  title: string;
  url: string;
  date: string;
  note: string;
  createdAt?: number;
  updatedAt?: number;
}

export interface SkillGoal {
  skill: string;
  goal: string;
  due: string;
}

export interface SourceResult {
  name: string;
  home: string;
  count: number;
  error?: string;
  skipped?: string;
}

export interface RadarSummary {
  date: string;
  started: string;
  finished: string;
  ok: boolean;
  sources: SourceResult[];
  problems: SourceResult[];
  scanned: number;
  matches: number;
  hidden: number;
  error?: string;
}

export interface RadarState {
  running: boolean;
  autoRun: boolean;
  last: RadarSummary | null;
  configError?: string;
  leads: Lead[];
}

export interface SkillRow {
  skill: string;
  area: string;
  status: "have" | "learning" | "gap";
  count: number;
  share: number;
  proof: number;
  goal?: string;
  due?: string;
}

export interface SkillReport {
  date: string;
  windowDays: number;
  listings: number;
  minJobs: number;
  enough: boolean;
  asked: number;
  coverage: number;
  gaps: SkillRow[];
  skills: SkillRow[];
  areas: { area: string; share: number; have: number; seen: number }[];
  mine: SkillRow[];
  proven: number;
  claimed: number;
}

export interface RadarSettings {
  config: string;
  skills: string;
  defaultConfig: string;
  defaultSkills: string;
  configError?: string;
  skillsError?: string;
  configIsCustom: boolean;
  skillsIsCustom: boolean;
}

export interface CompanyResult {
  ats: string;
  token: string;
  name: string;
  jobs: number;
  remote: number;
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public body: unknown,
  ) {
    super(message);
  }
}

// Only the remote listener returns 401, when the sign-in has expired or the
// password changed. Send the browser to the sign-in page and back here after.
function signedOut(res: Response) {
  if (res.status !== 401) return;
  window.location.href = "/login?next=" + encodeURIComponent(window.location.pathname + window.location.search);
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  signedOut(res);
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(data?.error || `Request failed (${res.status})`, res.status, data);
  return data as T;
}

export const api = {
  listApps: () => request<Application[]>("GET", "/api/applications"),
  check: (company: string, role: string, url: string) =>
    request<CheckResult>("POST", "/api/applications/check", { company, role, url }),
  createApp: (a: Partial<Application>, force = false) =>
    request<CreateResult>("POST", `/api/applications${force ? "?force=1" : ""}`, a),
  updateApp: (a: Partial<Application> & { id: string }, force = false) =>
    request<Application>("PUT", `/api/applications/${a.id}${force ? "?force=1" : ""}`, a),
  moveApp: (id: string, stage: string, outcome = "", skip?: Skip) =>
    request<Application>("PATCH", `/api/applications/${id}/stage`, { stage, outcome, skipReason: skip?.reason ?? "", skipNote: skip?.note ?? "" }),
  radar: () => request<RadarState>("GET", "/api/radar"),
  radarRun: () => request<RadarState>("POST", "/api/radar/run", {}),
  radarAct: (uid: string, stage: "saved" | "applied" | "skipped", skip?: Skip, force = false) =>
    request<CreateResult>("POST", "/api/radar/act", { uid, stage, skipReason: skip?.reason ?? "", skipNote: skip?.note ?? "", force }),
  radarSkills: () => request<SkillReport>("GET", "/api/radar/skills"),
  radarSettings: () => request<RadarSettings>("GET", "/api/radar/settings"),
  saveRadarSettings: (s: { config?: string; skills?: string }) => request<RadarSettings>("PUT", "/api/radar/settings", s),
  radarAddCompany: (ref: string, name: string) => request<CompanyResult>("POST", "/api/radar/companies", { ref, name }),
  evidence: () => request<{ evidence: Evidence[]; goals: SkillGoal[] }>("GET", "/api/radar/evidence"),
  saveEvidence: (e: Partial<Evidence>) =>
    request<Evidence>(e.id ? "PUT" : "POST", e.id ? `/api/radar/evidence/${encodeURIComponent(e.id)}` : "/api/radar/evidence", e),
  deleteEvidence: (id: string) => request<void>("DELETE", `/api/radar/evidence/${encodeURIComponent(id)}`),
  setGoal: (g: SkillGoal) => request<SkillGoal>("PUT", "/api/radar/goal", g),
  deleteApp: (id: string) => request<void>("DELETE", `/api/applications/${id}`),
  listDocs: () => request<Doc[]>("GET", "/api/documents"),
  saveDoc: (d: Partial<Doc>) => request<Doc>(d.id ? "PUT" : "POST", d.id ? `/api/documents/${d.id}` : "/api/documents", d),
  deleteDoc: (id: string) => request<void>("DELETE", `/api/documents/${id}`),
  settings: () => request<Settings>("GET", "/api/settings"),
  saveSettings: (s: { email?: string; dbPath?: string }) => request<Settings>("PUT", "/api/settings", s),
  session: () => request<{ remote: boolean }>("GET", "/api/session"),
  async upload(file: File): Promise<FileMeta> {
    const fd = new FormData();
    fd.append("file", file);
    const res = await fetch("/api/files", { method: "POST", body: fd });
    signedOut(res);
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new ApiError(data?.error || "Upload failed", res.status, data);
    return data;
  },
};

export const fileUrl = (f: FileMeta) => `/api/files/${f.id}`;

export const kb = (n: number) => (n >= 1048576 ? (n / 1048576).toFixed(1) + " MB" : Math.max(1, Math.round(n / 1024)) + " KB");

export function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function daysSince(s: string) {
  if (!s) return null;
  return Math.round((new Date(today() + "T00:00:00").getTime() - new Date(s + "T00:00:00").getTime()) / 864e5);
}

export const fmtDay = (s: string) => (s ? new Date(s + "T00:00:00").toLocaleDateString(undefined, { day: "numeric", month: "short" }) : "");

export function taggedEmail(base: string, ref: string) {
  const m = (base || "").trim().match(/^([^@+\s]+)(?:\+[^@]*)?@([^@\s]+)$/);
  return m ? `${m[1]}+${ref}@${m[2]}` : "";
}
