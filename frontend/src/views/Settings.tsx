import { useCallback, useEffect, useState } from "react";
import { AccountsService, DiagnosticsService } from "../../bindings/ghinbox/internal/services";
import type { AccountView } from "../../bindings/ghinbox/internal/services";
import { Rules } from "../../bindings/ghinbox/internal/filter";
import { fmtDateTime } from "../lib/format";
import { Button, Card, Chip, ErrorText, errMsg } from "../lib/ui";

const WRITE_TOOLS = ["dismiss_notification (mark read / done)", "manage_notification_subscription (mute a thread)"];

export default function Settings({ onAccountsChanged }: { onAccountsChanged: () => void }) {
  const [accounts, setAccounts] = useState<AccountView[]>([]);
  const [token, setToken] = useState("");
  const [host, setHost] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [rules, setRules] = useState<Rules | null>(null);
  const [rulesSaved, setRulesSaved] = useState("");

  const load = useCallback(() => {
    AccountsService.List()
      .then((a) => setAccounts(a ?? []))
      .catch((e) => setError(errMsg(e)));
    DiagnosticsService.FilterRules()
      .then(setRules)
      .catch((e) => setError(errMsg(e)));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const add = () => {
    setBusy(true);
    setError("");
    AccountsService.Add(token, host)
      .then((a) => {
        setToken("");
        setHost("");
        load();
        onAccountsChanged();
        console.info("account added", a.login);
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };

  const remove = (id: number, login: string) => {
    if (!window.confirm(`Remove account ${login}, its token and all synced data?`)) return;
    AccountsService.Remove(id)
      .then(() => {
        load();
        onAccountsChanged();
      })
      .catch((e) => setError(errMsg(e)));
  };

  const setMode = (id: number, login: string, mode: string) => {
    if (mode === "notifications") {
      const ok = window.confirm(
        `Enable GitHub writes for ${login}?\n\nThe MCP server restarts WITHOUT --read-only and exactly these tools become callable:\n  • ${WRITE_TOOLS.join("\n  • ")}\n\nNothing that touches issues, pull requests, comments, labels or repositories is enabled. Your token also needs notifications: write.`,
      );
      if (!ok) return;
    }
    AccountsService.SetWriteMode(id, mode)
      .then(load)
      .catch((e) => setError(errMsg(e)));
  };

  const saveRules = () => {
    if (!rules) return;
    DiagnosticsService.SetFilterRules(rules)
      .then(() => setRulesSaved("Saved. Rules apply to threads on their next sync."))
      .catch((e) => setError(errMsg(e)));
  };

  const listField = (v: string[] | null | undefined) => (v ?? []).join("\n");
  const parseList = (s: string) =>
    s
      .split("\n")
      .map((x) => x.trim())
      .filter(Boolean);

  return (
    <div className="max-w-3xl space-y-4">
      {error && <ErrorText>{error}</ErrorText>}

      <Card title="Accounts">
        {accounts.length === 0 && <p className="text-sm text-neutral-500">No accounts yet.</p>}
        <ul className="divide-y divide-neutral-200 dark:divide-neutral-800">
          {accounts.map(({ account: a, sync: s }) => (
            <li key={a.id} className="flex flex-wrap items-center gap-3 py-2 text-sm">
              <span className="font-medium">{a.login}</span>
              <span className="text-xs text-neutral-500">{a.host}</span>
              <label className="flex items-center gap-1 text-xs">
                writes:
                <select
                  className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900"
                  value={a.writeMode}
                  onChange={(e) => setMode(a.id, a.login, e.target.value)}
                >
                  <option value="readonly">read-only (default)</option>
                  <option value="notifications">notifications only</option>
                </select>
              </label>
              {a.writeMode === "readonly" ? <Chip tone="green">server --read-only</Chip> : <Chip tone="amber">writes: notifications</Chip>}
              <span className="text-xs text-neutral-500">last sync {fmtDateTime(s.lastSyncAt)}</span>
              {s.lastError && <span className="text-xs text-red-600">{s.lastError}</span>}
              <span className="ml-auto">
                <Button kind="danger" onClick={() => remove(a.id, a.login)}>
                  Remove
                </Button>
              </span>
            </li>
          ))}
        </ul>
      </Card>

      <Card title="Add account">
        <p className="mb-2 text-xs text-neutral-500">
          Paste a fine-grained personal access token (Notifications: read; repository Metadata, Issues, Pull requests: read). The token is
          validated with <code>get_me</code>, stored in the OS keyring, and never shown again.
        </p>
        <div className="flex flex-wrap gap-2">
          <input
            type="password"
            className="min-w-64 flex-1 rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
            placeholder="github_pat_…"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
          />
          <input
            className="w-48 rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
            placeholder="host (default github.com)"
            value={host}
            onChange={(e) => setHost(e.target.value)}
          />
          <Button kind="primary" onClick={add} disabled={busy || !token.trim()}>
            {busy ? "Checking…" : "Add"}
          </Button>
        </div>
      </Card>

      {rules && (
        <Card
          title="Noise filter"
          actions={
            <Button kind="primary" onClick={saveRules}>
              Save rules
            </Button>
          }
        >
          <div className="grid gap-3 text-sm md:grid-cols-2">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={rules.dropBotUpdates} onChange={(e) => setRules({ ...rules, dropBotUpdates: e.target.checked } as Rules)} />
              Hide dependency-bot threads (dependabot / renovate titles)
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={rules.dropCiSuccess} onChange={(e) => setRules({ ...rules, dropCiSuccess: e.target.checked } as Rules)} />
              Hide successful CI runs
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={rules.dropReleases} onChange={(e) => setRules({ ...rules, dropReleases: e.target.checked } as Rules)} />
              Hide releases except from repos below
            </label>
            <div />
            <TextList label="Repos whose releases to keep (owner/name)" value={listField(rules.keepReleaseRepos)} onChange={(v) => setRules({ ...rules, keepReleaseRepos: parseList(v) } as Rules)} />
            <TextList label="Muted repos (owner/name or owner/*)" value={listField(rules.mutedRepos)} onChange={(v) => setRules({ ...rules, mutedRepos: parseList(v) } as Rules)} />
            <TextList label="Muted title keywords" value={listField(rules.mutedTitleKeywords)} onChange={(v) => setRules({ ...rules, mutedTitleKeywords: parseList(v) } as Rules)} />
          </div>
          {rulesSaved && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{rulesSaved}</p>}
        </Card>
      )}
    </div>
  );
}

function TextList({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="block text-xs text-neutral-600 dark:text-neutral-400">
      {label}
      <textarea
        className="mt-1 h-20 w-full rounded border border-neutral-300 bg-white p-1 font-mono text-xs dark:border-neutral-700 dark:bg-neutral-900"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="one per line"
      />
    </label>
  );
}
