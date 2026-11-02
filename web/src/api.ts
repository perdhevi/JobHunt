export const STAGES = [
  { id: "saved", name: "Saved" },
  { id: "applied", name: "Applied" },
  { id: "screening", name: "Screening" },
  { id: "interview", name: "Interviewing" },
  { id: "offer", name: "Offer" },
  { id: "closed", name: "Closed" },
] as const;

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
}

export interface CheckResult {
  verdict: "new" | "possible_duplicate" | "already_tracked" | "already_sent";
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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
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
  moveApp: (id: string, stage: string, outcome = "") =>
    request<Application>("PATCH", `/api/applications/${id}/stage`, { stage, outcome }),
  deleteApp: (id: string) => request<void>("DELETE", `/api/applications/${id}`),
  listDocs: () => request<Doc[]>("GET", "/api/documents"),
  saveDoc: (d: Partial<Doc>) => request<Doc>(d.id ? "PUT" : "POST", d.id ? `/api/documents/${d.id}` : "/api/documents", d),
  deleteDoc: (id: string) => request<void>("DELETE", `/api/documents/${id}`),
  settings: () => request<Settings>("GET", "/api/settings"),
  saveSettings: (s: { email?: string; dbPath?: string }) => request<Settings>("PUT", "/api/settings", s),
  async upload(file: File): Promise<FileMeta> {
    const fd = new FormData();
    fd.append("file", file);
    const res = await fetch("/api/files", { method: "POST", body: fd });
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
