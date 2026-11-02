import { FormEvent, useState } from "react";
import { api, Settings } from "./api";

export function SettingsPanel({ settings, onClose, onSaved }: { settings: Settings; onClose: () => void; onSaved: (s: Settings) => void }) {
  const [email, setEmail] = useState(settings.email);
  const [dbPath, setDbPath] = useState(settings.dbPath);
  const [msg, setMsg] = useState({ text: "", err: false });
  const restart = settings.docker ? "docker compose restart" : "jobhunt";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const s = await api.saveSettings({ email, dbPath });
      onSaved(s);
      setDbPath(s.dbPath);
      setMsg({ text: s.dbPath === s.activePath ? "Saved." : `Saved. Restart ${restart} to switch databases.`, err: false });
    } catch (err) {
      setMsg({ text: err instanceof Error ? err.message : "Couldn't save.", err: true });
    }
  };

  return (
    <div className="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <form className="panel" onSubmit={submit} aria-labelledby="sTitle">
        <div className="lhead"><h2 id="sTitle">Settings</h2><button type="button" className="btn" onClick={onClose}>Done</button></div>
        <div className="f">
          <label htmlFor="s-db">Database file</label>
          <input id="s-db" required value={dbPath} onChange={(e) => setDbPath(e.target.value)} placeholder="G:\My Drive\JobHunt\jobhunt.db" />
          <span className="hint">Full path to the SQLite file. A new file is created if none exists, and the attachments folder sits beside it. Existing data isn't moved. To move, stop jobhunt and copy the .db and attachments folder first.</span>
        </div>
        <div className="f">
          <label>In use now</label>
          <code className="path">{settings.activePath}</code>
          {settings.dbPath !== settings.activePath && <span className="status err">Restart {restart} to apply the saved path.</span>}
        </div>
        <div className="f">
          <label htmlFor="s-email">Base email</label>
          <input id="s-email" type="email" placeholder="you@gmail.com" value={email} onChange={(e) => setEmail(e.target.value)} />
          <span className="hint">Each application gets its own address with a reference code after a "+". Gmail delivers it to your normal inbox.</span>
        </div>
        <div className="f">
          <label>Config file</label>
          <code className="path">{settings.configPath}</code>
        </div>
        {msg.text && <p className={"status" + (msg.err ? " err" : "")} aria-live="polite">{msg.text}</p>}
        <div className="actions"><span /><button type="submit" className="btn primary">Save</button></div>
      </form>
    </div>
  );
}
