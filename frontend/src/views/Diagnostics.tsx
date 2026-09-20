import { useEffect, useState } from "react";
import { DiagnosticsService, JudgeService } from "../../bindings/gitinbox/internal/services";
import type { Environment, JudgeStatus, MCPServerInfo } from "../../bindings/gitinbox/internal/services";
import type { Stats, ToolInfo } from "../../bindings/gitinbox/internal/ghmcp";
import type { Account, UsageRow } from "../../bindings/gitinbox/internal/store";
import { fmtDateTime } from "../lib/format";
import { Button, Card, Chip, ErrorText, errMsg } from "../lib/ui";

export default function Diagnostics({ accounts: allAccounts }: { accounts: Account[] }) {
  const accounts = allAccounts.filter((a) => a.forge !== "gitlab");
  const gitlabAccounts = allAccounts.filter((a) => a.forge === "gitlab");
  const [mcp, setMcp] = useState<MCPServerInfo | null>(null);
  const [env, setEnv] = useState<Environment | null>(null);
  const [tools, setTools] = useState<ToolInfo[]>([]);
  const [stats, setStats] = useState<Record<string, Stats | undefined>>({});
  const [selected, setSelected] = useState<number>(accounts[0]?.id ?? 0);
  const [judge, setJudge] = useState<JudgeStatus | null>(null);
  const [usage, setUsage] = useState<UsageRow[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    DiagnosticsService.MCPServerInfo().then(setMcp).catch((e) => setError(errMsg(e)));
    DiagnosticsService.Environment().then(setEnv).catch((e) => setError(errMsg(e)));
    JudgeService.Status().then(setJudge).catch((e) => setError(errMsg(e)));
    DiagnosticsService.Usage()
      .then((u) => setUsage(u ?? []))
      .catch((e) => setError(errMsg(e)));
    DiagnosticsService.ClientStats()
      .then((s) => setStats((s ?? {}) as Record<string, Stats | undefined>))
      .catch((e) => setError(errMsg(e)));
  }, []);

  useEffect(() => {
    if (!selected && accounts[0]) setSelected(accounts[0].id);
  }, [accounts, selected]);

  const loadTools = () => {
    if (!selected) return;
    setBusy(true);
    setError("");
    DiagnosticsService.Tools(selected)
      .then((t) => setTools(t ?? []))
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };

  const allowed = tools.filter((t) => t.allowed).length;
  const writes = tools.filter((t) => !t.readOnly).length;

  return (
    <div className="max-w-4xl space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      <div className="grid gap-4 md:grid-cols-2">
        <Card title="GitHub MCP server">
          {mcp?.error ? (
            <ErrorText>{mcp.error}</ErrorText>
          ) : (
            <dl className="grid grid-cols-[6rem_1fr] gap-y-1 text-sm">
              <dt className="text-neutral-500">Source</dt>
              <dd>{mcp?.source}</dd>
              <dt className="text-neutral-500">Version</dt>
              <dd>{mcp?.version || "unknown"}</dd>
              <dt className="text-neutral-500">Path</dt>
              <dd className="break-all font-mono text-xs">{mcp?.path}</dd>
            </dl>
          )}
        </Card>
        <Card title="Environment">
          <dl className="grid grid-cols-[6rem_1fr] gap-y-1 text-sm">
            <dt className="text-neutral-500">Secrets</dt>
            <dd>{env?.secretsBackend}</dd>
            <dt className="text-neutral-500">Database</dt>
            <dd className="break-all font-mono text-xs">{env?.dbPath}</dd>
            <dt className="text-neutral-500">Log</dt>
            <dd className="break-all font-mono text-xs">{env?.logPath}</dd>
          </dl>
        </Card>
      </div>

      {gitlabAccounts.length > 0 && (
        <Card title="GitLab accounts">
          <ul className="text-sm">
            {gitlabAccounts.map((a) => (
              <li key={a.id} className="flex flex-wrap gap-3 py-1">
                <span className="font-medium">{a.login}</span>
                <span className="text-neutral-500">{a.host}</span>
                <span className="text-neutral-500">REST via official client (no MCP server)</span>
                <span className="text-neutral-500">token scopes: {a.tokenScopes || "unknown"}</span>
                <span className="text-neutral-500">write mode: {a.writeMode}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card
        title="Server tools (GitHub accounts)"
        actions={
          <>
            <select
              className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900"
              value={selected}
              onChange={(e) => setSelected(Number(e.target.value))}
            >
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.login} ({a.writeMode})
                </option>
              ))}
            </select>
            <Button kind="primary" onClick={loadTools} disabled={busy || !selected}>
              {busy ? "Loading…" : "List tools"}
            </Button>
          </>
        }
      >
        {tools.length > 0 && (
          <>
            <p className="mb-2 text-xs text-neutral-500">
              {tools.length} tools registered by the server; {writes} write tools exposed; {allowed} on this app's allowlist.
            </p>
            <table className="w-full text-left text-xs">
              <thead className="text-neutral-500">
                <tr>
                  <th className="py-1 pr-2">Tool</th>
                  <th className="py-1 pr-2">Server</th>
                  <th className="py-1 pr-2">App allowlist</th>
                  <th className="py-1">Title</th>
                </tr>
              </thead>
              <tbody>
                {tools.map((t) => (
                  <tr key={t.name} className="border-t border-neutral-100 dark:border-neutral-800">
                    <td className="py-1 pr-2 font-mono">{t.name}</td>
                    <td className="py-1 pr-2">{t.readOnly ? <Chip tone="green">read-only</Chip> : <Chip tone="amber">write</Chip>}</td>
                    <td className="py-1 pr-2">{t.allowed ? <Chip tone="blue">allowed</Chip> : <Chip>blocked</Chip>}</td>
                    <td className="py-1">{t.title}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </Card>

      <Card title="Triage judge">
        {judge && (
          <dl className="grid grid-cols-[8rem_1fr] gap-y-1 text-sm">
            <dt className="text-neutral-500">Provider</dt>
            <dd>{judge.provider ? `${judge.provider} (${judge.calibrated ? "calibrated" : "uncalibrated"})` : <span className="text-amber-700">{judge.error}</span>}</dd>
            <dt className="text-neutral-500">Jev key</dt>
            <dd>{judge.hasJevKey ? "stored" : "missing"}</dd>
            <dt className="text-neutral-500">Judgments</dt>
            <dd>
              {judge.stats.total} total ·{" "}
              {Object.entries(judge.stats.byProvider ?? {})
                .map(([k, v]) => `${k}: ${v}`)
                .join(", ") || "none"}
            </dd>
            <dt className="text-neutral-500">Tokens</dt>
            <dd>
              {judge.stats.inputTokens} in / {judge.stats.outputTokens} out
            </dd>
          </dl>
        )}
      </Card>

      <Card title="Model usage (stored generations)">
        {usage.length === 0 && <p className="text-sm text-neutral-500">Nothing generated yet.</p>}
        {usage.length > 0 && (
          <table className="w-full text-left text-xs">
            <thead className="text-neutral-500">
              <tr>
                <th className="py-1 pr-2">Source</th>
                <th className="py-1 pr-2">Provider</th>
                <th className="py-1 pr-2">Model</th>
                <th className="py-1 pr-2">Count</th>
                <th className="py-1 pr-2">Tokens in</th>
                <th className="py-1 pr-2">Tokens out</th>
                <th className="py-1">Avg latency</th>
              </tr>
            </thead>
            <tbody>
              {usage.map((r, i) => (
                <tr key={i} className="border-t border-neutral-100 dark:border-neutral-800">
                  <td className="py-1 pr-2">{r.source}</td>
                  <td className="py-1 pr-2">{r.provider}</td>
                  <td className="py-1 pr-2 font-mono">{r.model}</td>
                  <td className="py-1 pr-2">{r.count}</td>
                  <td className="py-1 pr-2">{r.inputTokens}</td>
                  <td className="py-1 pr-2">{r.outputTokens}</td>
                  <td className="py-1">{Math.round(r.avgLatencyMs)} ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card title="MCP call counters (this session)">
        {Object.keys(stats).length === 0 && <p className="text-sm text-neutral-500">No server started yet.</p>}
        {Object.entries(stats).map(([id, s]) => (
          <div key={id} className="mb-2 text-xs">
            <div className="font-medium">
              account {id} · started {fmtDateTime(s?.startedAt)} · restarts {s?.restarts} · rejected {s?.rejected}
            </div>
            <div className="font-mono text-neutral-600 dark:text-neutral-400">
              {Object.entries(s?.calls ?? {})
                .map(([k, v]) => `${k}=${v}`)
                .join("  ") || "no calls"}
            </div>
            {s?.lastError && <div className="text-red-600">{s.lastError}</div>}
          </div>
        ))}
      </Card>
    </div>
  );
}
