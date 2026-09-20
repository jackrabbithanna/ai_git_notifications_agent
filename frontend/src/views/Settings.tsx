import { useCallback, useEffect, useState } from "react";
import { AccountsService, DiagnosticsService, ImpactService, JudgeService, ProseService, WatchesService } from "../../bindings/ghinbox/internal/services";
import type { ProseSettings } from "../../bindings/ghinbox/internal/pipeline";
import type { ImpactSettings } from "../../bindings/ghinbox/internal/pipeline";
import type { JudgeStatus } from "../../bindings/ghinbox/internal/services";
import type { JudgeSettings } from "../../bindings/ghinbox/internal/pipeline";
import type { Weights } from "../../bindings/ghinbox/internal/scoring";
import type { WatchedProject } from "../../bindings/ghinbox/internal/store";
import type { AccountView } from "../../bindings/ghinbox/internal/services";
import { Rules } from "../../bindings/ghinbox/internal/filter";
import { fmtDateTime } from "../lib/format";
import { Button, Card, Chip, ErrorText, errMsg } from "../lib/ui";

const WRITE_TOOLS = ["dismiss_notification (mark read / done)", "manage_notification_subscription (mute a thread)"];

export default function Settings({ onAccountsChanged }: { onAccountsChanged: () => void }) {
  const [accounts, setAccounts] = useState<AccountView[]>([]);
  const [forge, setForge] = useState<"github" | "gitlab">("github");
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
    AccountsService.Add(forge, token, host)
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
              <Chip tone={a.forge === "gitlab" ? "amber" : "neutral"}>{a.forge === "gitlab" ? "GitLab" : "GitHub"}</Chip>
              <span className="font-medium">{a.login}</span>
              <span className="text-xs text-neutral-500">{a.host}</span>
              {a.tokenScopes && <span className="text-xs text-neutral-500">scopes: {a.tokenScopes}</span>}
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
              {a.forge === "gitlab" && <WatchedProjects accountId={a.id} onError={setError} />}
            </li>
          ))}
        </ul>
      </Card>

      <Card title="Add account">
        <p className="mb-2 text-xs text-neutral-500">
          {forge === "github"
            ? "Paste a personal access token (classic: notifications scope; fine-grained: Notifications read + repo Metadata/Issues/Pull requests read). Validated with get_me."
            : "Paste a GitLab personal access token with the read_api scope (api only if you later enable writes). Validated with GET /user; the token's scopes are recorded."}{" "}
          Stored in the OS keyring and never shown again.
        </p>
        <div className="flex flex-wrap gap-2">
          <select
            className="rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
            value={forge}
            onChange={(e) => setForge(e.target.value as "github" | "gitlab")}
          >
            <option value="github">GitHub</option>
            <option value="gitlab">GitLab</option>
          </select>
          <input
            type="password"
            className="min-w-64 flex-1 rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
            placeholder={forge === "github" ? "github_pat_… / ghp_…" : "glpat-…"}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
          />
          <input
            className="w-56 rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
            placeholder={forge === "github" ? "host (default github.com)" : "host (default gitlab.com), e.g. lab.civicrm.org"}
            value={host}
            onChange={(e) => setHost(e.target.value)}
          />
          <Button kind="primary" onClick={add} disabled={busy || !token.trim()}>
            {busy ? "Checking…" : "Add"}
          </Button>
        </div>
      </Card>

      <JudgeSection onError={setError} />
      <WeightsSection onError={setError} />
      <ImpactSection onError={setError} />
      <ProseSection onError={setError} />

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

function JudgeSection({ onError }: { onError: (e: string) => void }) {
  const [status, setStatus] = useState<JudgeStatus | null>(null);
  const [s, setS] = useState<JudgeSettings | null>(null);
  const [jevKey, setJevKey] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [saved, setSaved] = useState("");
  const load = useCallback(() => {
    JudgeService.Status()
      .then((st) => {
        setStatus(st);
        setS(st.settings);
      })
      .catch((e) => onError(errMsg(e)));
  }, [onError]);
  useEffect(() => {
    load();
  }, [load]);
  if (!s || !status) return null;
  const save = () => {
    JudgeService.SaveSettings(s)
      .then(() => {
        setSaved("Saved.");
        load();
      })
      .catch((e) => onError(errMsg(e)));
  };
  const saveKey = () => {
    JudgeService.SetJevKey(jevKey)
      .then(() => {
        setJevKey("");
        setSaved(jevKey ? "Jev key stored in the keyring." : "Jev key removed.");
        load();
      })
      .catch((e) => onError(errMsg(e)));
  };
  const listModels = () => {
    JudgeService.OllamaModels(s.ollamaUrl)
      .then((m) => setModels(m ?? []))
      .catch((e) => onError(errMsg(e)));
  };
  const input = "rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900";
  return (
    <Card
      title="Triage judge"
      actions={
        <Button kind="primary" onClick={save}>
          Save judge settings
        </Button>
      }
    >
      <p className="mb-2 text-xs text-neutral-500">
        {status.provider ? (
          <>
            Active provider: <b>{status.provider}</b> ({status.calibrated ? "calibrated probabilities" : "uncalibrated — discounted in scoring"}) · {status.stats.total} judgments stored ·
            tokens {status.stats.inputTokens}/{status.stats.outputTokens}
          </>
        ) : (
          <span className="text-amber-700 dark:text-amber-300">No judge active: {status.error}</span>
        )}
      </p>
      <div className="grid gap-3 text-sm md:grid-cols-2">
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Provider
          <select className={"mt-1 w-full " + input} value={s.provider} onChange={(e) => setS({ ...s, provider: e.target.value })}>
            <option value="auto">auto (Jev if key stored, else Ollama)</option>
            <option value="jev">Jev (TypeSafe, calibrated)</option>
            <option value="ollama">Ollama (local, uncalibrated)</option>
            <option value="off">off</option>
          </select>
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          What I work on (given to the judge as profile.interests)
          <textarea className={"mt-1 h-20 w-full " + input} value={s.profileInterests} onChange={(e) => setS({ ...s, profileInterests: e.target.value })} placeholder="e.g. CiviCRM extensions using APIv4, Drupal integration, Afform/SearchKit…" />
        </label>
        <div className="text-xs text-neutral-600 dark:text-neutral-400">
          Jev API key {status.hasJevKey ? <Chip tone="green">stored</Chip> : <Chip tone="amber">missing</Chip>}
          <div className="mt-1 flex gap-1">
            <input type="password" className={"flex-1 " + input} placeholder="paste key (empty = remove)" value={jevKey} onChange={(e) => setJevKey(e.target.value)} autoComplete="off" />
            <Button onClick={saveKey}>Store</Button>
          </div>
          <label className="mt-2 block">
            Jev model
            <input className={"mt-1 w-full " + input} value={s.jevModel} onChange={(e) => setS({ ...s, jevModel: e.target.value })} />
          </label>
        </div>
        <div className="text-xs text-neutral-600 dark:text-neutral-400">
          Ollama URL
          <div className="mt-1 flex gap-1">
            <input className={"flex-1 " + input} value={s.ollamaUrl} onChange={(e) => setS({ ...s, ollamaUrl: e.target.value })} placeholder="http://gpu-box:11434" />
            <Button onClick={listModels}>List models</Button>
          </div>
          <label className="mt-2 block">
            Ollama judge model
            <input className={"mt-1 w-full " + input} list="ollama-models" value={s.ollamaJudgeModel} onChange={(e) => setS({ ...s, ollamaJudgeModel: e.target.value })} placeholder="e.g. qwen3:8b" />
            <datalist id="ollama-models">
              {models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </label>
        </div>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Max threads judged per run
          <input type="number" className={"mt-1 w-full " + input} value={s.maxPerRun} onChange={(e) => setS({ ...s, maxPerRun: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Concurrency
          <input type="number" className={"mt-1 w-full " + input} value={s.concurrency} onChange={(e) => setS({ ...s, concurrency: Number(e.target.value) })} />
        </label>
      </div>
      {saved && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{saved}</p>}
    </Card>
  );
}

function ProseSection({ onError }: { onError: (e: string) => void }) {
  const [s, setS] = useState<ProseSettings | null>(null);
  const [saved, setSaved] = useState("");
  useEffect(() => {
    ProseService.Settings().then(setS).catch((e) => onError(errMsg(e)));
  }, [onError]);
  if (!s) return null;
  const input = "rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900";
  const save = () =>
    ProseService.SaveSettings(s)
      .then(() => setSaved("Saved."))
      .catch((e) => onError(errMsg(e)));
  const testNotify = () =>
    ProseService.TestNotification()
      .then(() => setSaved("Test notification sent."))
      .catch((e) => onError(errMsg(e)));
  return (
    <Card
      title="Summaries, digest & notifications"
      actions={
        <>
          <Button onClick={testNotify}>Test notification</Button>
          <Button kind="primary" onClick={save}>
            Save prose settings
          </Button>
        </>
      }
    >
      <div className="grid gap-3 text-sm md:grid-cols-3">
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Ollama model for summaries (blank = note/judge model)
          <input className={"mt-1 w-full " + input} value={s.summaryModel} onChange={(e) => setS({ ...s, summaryModel: e.target.value })} placeholder="e.g. qwen3.5:9b" />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Auto-summarise top N threads per run (0 = off)
          <input type="number" className={"mt-1 w-full " + input} value={s.topN} onChange={(e) => setS({ ...s, topN: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          …at most every (minutes)
          <input type="number" className={"mt-1 w-full " + input} value={s.summarizeEveryMin} onChange={(e) => setS({ ...s, summarizeEveryMin: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Digest model (blank = summary model)
          <input className={"mt-1 w-full " + input} value={s.digestModel} onChange={(e) => setS({ ...s, digestModel: e.target.value })} />
        </label>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={s.autoDigest} onChange={(e) => setS({ ...s, autoDigest: e.target.checked })} /> Generate a digest automatically every
          <input type="number" className={"w-16 " + input} value={s.digestEveryH} onChange={(e) => setS({ ...s, digestEveryH: Number(e.target.value) })} /> h
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Threads fed to the digest
          <input type="number" className={"mt-1 w-full " + input} value={s.digestMaxThreads} onChange={(e) => setS({ ...s, digestMaxThreads: Number(e.target.value) })} />
        </label>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={s.notifyNeedsMe} onChange={(e) => setS({ ...s, notifyNeedsMe: e.target.checked })} /> Desktop notification when a thread enters "Needs me"
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Notify for impact analyses at level
          <select className={"mt-1 w-full " + input} value={s.notifyImpactMinLevel} onChange={(e) => setS({ ...s, notifyImpactMinLevel: Number(e.target.value) })}>
            <option value={2}>likely and above</option>
            <option value={3}>certain only</option>
            <option value={4}>never</option>
          </select>
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Max notifications per run
          <input type="number" className={"mt-1 w-full " + input} value={s.notifyMaxPerRun} onChange={(e) => setS({ ...s, notifyMaxPerRun: Number(e.target.value) })} />
        </label>
      </div>
      {saved && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{saved}</p>}
    </Card>
  );
}

function ImpactSection({ onError }: { onError: (e: string) => void }) {
  const [s, setS] = useState<ImpactSettings | null>(null);
  const [saved, setSaved] = useState("");
  useEffect(() => {
    ImpactService.Settings().then(setS).catch((e) => onError(errMsg(e)));
  }, [onError]);
  if (!s) return null;
  const input = "rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900";
  const save = () =>
    ImpactService.SaveSettings(s)
      .then(() => setSaved("Saved."))
      .catch((e) => onError(errMsg(e)));
  return (
    <Card
      title="PR impact analysis"
      actions={
        <Button kind="primary" onClick={save}>
          Save impact settings
        </Button>
      }
    >
      <div className="grid gap-3 text-sm md:grid-cols-3">
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={s.autoAnalyze} onChange={(e) => setS({ ...s, autoAnalyze: e.target.checked })} /> Analyse PR threads in profile repos automatically after each sync
        </label>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={s.scanLanded} onChange={(e) => setS({ ...s, scanLanded: e.target.checked })} /> Scan profile repos for recently merged PRs
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Max analyses per run
          <input type="number" className={"mt-1 w-full " + input} value={s.maxPerRun} onChange={(e) => setS({ ...s, maxPerRun: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Landed scan every (minutes)
          <input type="number" className={"mt-1 w-full " + input} value={s.scanLandedEveryMin} onChange={(e) => setS({ ...s, scanLandedEveryMin: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Landed look-back (days)
          <input type="number" className={"mt-1 w-full " + input} value={s.landedLookbackDays} onChange={(e) => setS({ ...s, landedLookbackDays: Number(e.target.value) })} />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Ollama model for impact notes (blank = judge model)
          <input className={"mt-1 w-full " + input} value={s.noteModel} onChange={(e) => setS({ ...s, noteModel: e.target.value })} placeholder="e.g. qwen3.5:9b" />
        </label>
        <label className="block text-xs text-neutral-600 dark:text-neutral-400">
          Write notes automatically at level ≥
          <select className={"mt-1 w-full " + input} value={s.autoNoteMinLevel} onChange={(e) => setS({ ...s, autoNoteMinLevel: Number(e.target.value) })}>
            <option value={2}>likely</option>
            <option value={3}>certain</option>
            <option value={4}>never (on demand only)</option>
          </select>
        </label>
      </div>
      {saved && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{saved}</p>}
    </Card>
  );
}

function WeightsSection({ onError }: { onError: (e: string) => void }) {
  const [w, setW] = useState<Weights | null>(null);
  const [saved, setSaved] = useState("");
  const load = useCallback(() => {
    JudgeService.Weights()
      .then(setW)
      .catch((e) => onError(errMsg(e)));
  }, [onError]);
  useEffect(() => {
    load();
  }, [load]);
  if (!w) return null;
  const save = (next: Weights) => {
    setW(next);
    JudgeService.SaveWeights(next)
      .then(() => setSaved("Saved — inbox re-ranked from stored judgments, no new inference."))
      .catch((e) => onError(errMsg(e)));
  };
  const slider = (label: string, key: keyof Weights, max = 2, step = 0.05) => (
    <label className="block text-xs text-neutral-600 dark:text-neutral-400">
      {label}: <b>{Number(w[key]).toFixed(2)}</b>
      <input type="range" min={0} max={max} step={step} className="mt-1 w-full" value={Number(w[key])} onChange={(e) => save({ ...w, [key]: Number(e.target.value) })} />
    </label>
  );
  return (
    <Card
      title="Priority weights"
      actions={
        <Button
          onClick={() =>
            JudgeService.DefaultWeights()
              .then(save)
              .catch((e) => onError(errMsg(e)))
          }
        >
          Reset to defaults
        </Button>
      }
    >
      <div className="grid gap-3 md:grid-cols-3">
        {slider("Requires my action", "action")}
        {slider("Urgency", "urgency")}
        {slider("Relevance to my profile", "relevance")}
        {slider("Recency", "recency", 1)}
        {slider("Resolved penalty", "resolved")}
        {slider("Recency half-life (hours)", "recencyHalfLifeH", 240, 1)}
        {slider("Resolved threshold", "resolvedThreshold", 1, 0.05)}
        {slider("'Unsure' below confidence", "unsureBelow", 1, 0.05)}
        {slider("Uncalibrated discount", "uncalibratedDiscount", 1, 0.05)}
      </div>
      {saved && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{saved}</p>}
    </Card>
  );
}

function WatchedProjects({ accountId, onError }: { accountId: number; onError: (e: string) => void }) {
  const [items, setItems] = useState<WatchedProject[]>([]);
  const [path, setPath] = useState("");
  const load = useCallback(() => {
    WatchesService.List(accountId)
      .then((w) => setItems(w ?? []))
      .catch((e) => onError(errMsg(e)));
  }, [accountId, onError]);
  useEffect(() => {
    load();
  }, [load]);
  const add = () => {
    const p = path.trim();
    if (!p) return;
    WatchesService.Add(accountId, p)
      .then(() => {
        setPath("");
        load();
      })
      .catch((e) => onError(errMsg(e)));
  };
  const remove = (p: string) =>
    WatchesService.Remove(accountId, p)
      .then(load)
      .catch((e) => onError(errMsg(e)));
  return (
    <div className="basis-full pl-2 text-xs">
      <div className="mb-1 text-neutral-500">Watched projects (activity polled each sync; GitLab has no notifications feed):</div>
      <ul className="mb-1 space-y-0.5">
        {items.map((w) => (
          <li key={w.path} className="flex items-center gap-2">
            <span className="font-mono">{w.path}</span>
            {w.projectId ? <span className="text-neutral-400">id {w.projectId}</span> : <span className="text-neutral-400">unresolved</span>}
            {w.lastEventAt && <span className="text-neutral-400">events to {fmtDateTime(w.lastEventAt)}</span>}
            {w.lastError && <span className="text-red-600">{w.lastError}</span>}
            <Button kind="ghost" onClick={() => remove(w.path)}>
              remove
            </Button>
          </li>
        ))}
        {items.length === 0 && <li className="text-neutral-400">none</li>}
      </ul>
      <div className="flex gap-1">
        <input
          className="w-64 rounded border border-neutral-300 bg-white px-2 py-0.5 dark:border-neutral-700 dark:bg-neutral-900"
          placeholder="group/project, e.g. dev/core"
          value={path}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && add()}
        />
        <Button onClick={add} disabled={!path.trim()}>
          Watch
        </Button>
      </div>
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
