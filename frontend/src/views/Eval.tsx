import { useCallback, useEffect, useState } from "react";
import { EvalService } from "../../bindings/gitinbox/internal/services";
import type { EvalOverview, LabelQueueItem } from "../../bindings/gitinbox/internal/pipeline";
import type { Label, Account } from "../../bindings/gitinbox/internal/store";
import type { Report, TuneResult } from "../../bindings/gitinbox/internal/eval";
import { openURL } from "../lib/browser";
import { timeAgo } from "../lib/format";
import { Button, Card, Chip, ErrorText, errMsg } from "../lib/ui";

const CATEGORIES = ["needs_my_review", "needs_my_reply", "blocking_or_failing", "awaiting_others", "fyi_progress", "release_or_announcement", "resolved_no_action"];

export default function Eval({ refreshKey, accountId, accounts }: { refreshKey: number; accountId: number; accounts: Account[] }) {
  const [queue, setQueue] = useState<LabelQueueItem[]>([]);
  const [includeLabeled, setIncludeLabeled] = useState(false);
  const [overview, setOverview] = useState<EvalOverview | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [reportMd, setReportMd] = useState("");
  const [provider, setProvider] = useState("primary");
  const [judgeProvider, setJudgeProvider] = useState("ollama");
  const [tune, setTune] = useState<TuneResult | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(() => {
    EvalService.Queue(accountId, 40, includeLabeled)
      .then((q) => setQueue(q ?? []))
      .catch((e) => setError(errMsg(e)));
    EvalService.Overview().then(setOverview).catch((e) => setError(errMsg(e)));
  }, [accountId, includeLabeled]);
  useEffect(() => {
    load();
  }, [load, refreshKey]);

  const run = (what: string, p: Promise<unknown>) => {
    setBusy(what);
    setError("");
    setNotice("");
    p.catch((e) => setError(errMsg(e))).finally(() => {
      setBusy("");
      load();
    });
  };
  const evaluate = () => run("evaluate", EvalService.Evaluate(provider).then(setReport));
  const evalJudge = () =>
    run(
      "judge",
      EvalService.EvalJudge(judgeProvider, "", false).then((r) => setNotice(`${r.provider} ${r.model}: judged ${r.judged}, skipped ${r.skipped}, failed ${r.failed}${r.errors?.length ? " — " + r.errors.join("; ") : ""}`)),
    );
  const doTune = (apply: boolean) => run("tune", EvalService.Tune(400, apply).then(setTune));
  const doReport = () => run("report", EvalService.Report().then(setReportMd));

  return (
    <div className="space-y-4">
      <Card
        title="Evaluation"
        actions={
          <>
            <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="primary">primary judgments</option>
              {Object.keys(overview?.providers ?? {}).map((p) => (
                <option key={p} value={p}>
                  eval: {p}
                </option>
              ))}
            </select>
            <Button kind="primary" onClick={evaluate} disabled={!!busy}>
              Evaluate
            </Button>
            <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={judgeProvider} onChange={(e) => setJudgeProvider(e.target.value)}>
              <option value="ollama">Ollama (judge model)</option>
              <option value="jev">Jev</option>
            </select>
            <Button onClick={evalJudge} disabled={!!busy} title="Judge every labeled thread with this provider into the eval table (does not touch the primary judgments)">
              {busy === "judge" ? "Judging…" : "Re-judge labeled set"}
            </Button>
            <Button onClick={() => doTune(false)} disabled={!!busy}>
              Tune (dry run)
            </Button>
            <Button onClick={() => doTune(true)} disabled={!!busy} title="Search weights that maximise NDCG@25 on your priority labels and store them">
              Tune & apply
            </Button>
            <Button onClick={doReport} disabled={!!busy}>
              Report
            </Button>
          </>
        }
      >
        {overview && (
          <p className="text-xs text-neutral-500">
            {overview.labels} labeled threads ({overview.primary} with a primary judgment) · {overview.prLabels} labeled PRs · eval judgments:{" "}
            {Object.entries(overview.providers ?? {})
              .map(([k, v]) => `${k} ${v}`)
              .join(", ") || "none"}
            . Aim for ~150 threads and ~50 PRs; label priority (0 ignore … 3 top) on every thread for the ranking metric.
          </p>
        )}
        {error && <ErrorText>{error}</ErrorText>}
        {notice && <p className="text-xs">{notice}</p>}
        {report && <ReportTable r={report} />}
        {tune && (
          <p className="mt-2 text-xs">
            Tune: objective {tune.ndcgFrom.toFixed(3)} → {tune.ndcgTo.toFixed(3)} over {tune.labeled} labeled · action {tune.before.action.toFixed(2)}→{tune.after.action.toFixed(2)}, urgency{" "}
            {tune.before.urgency.toFixed(2)}→{tune.after.urgency.toFixed(2)}, relevance {tune.before.relevance.toFixed(2)}→{tune.after.relevance.toFixed(2)}, recency {tune.before.recency.toFixed(2)}→
            {tune.after.recency.toFixed(2)}, resolved {tune.before.resolved.toFixed(2)}→{tune.after.resolved.toFixed(2)} (thr {tune.before.resolvedThreshold.toFixed(2)}→{tune.after.resolvedThreshold.toFixed(2)}), impact{" "}
            {tune.before.impact.toFixed(2)}→{tune.after.impact.toFixed(2)}
          </p>
        )}
        {reportMd && <pre className="mt-2 max-h-96 overflow-auto rounded bg-neutral-50 p-2 text-[11px] dark:bg-neutral-950">{reportMd}</pre>}
      </Card>

      <Card
        title="Label queue"
        actions={
          <label className="flex items-center gap-1 text-xs">
            <input type="checkbox" checked={includeLabeled} onChange={(e) => setIncludeLabeled(e.target.checked)} /> show labeled
          </label>
        }
      >
        <ul className="divide-y divide-neutral-200 dark:divide-neutral-800">
          {queue.map((it) => (
            <LabelRow key={`${it.thread.accountId}:${it.thread.threadId}`} item={it} accounts={accounts} onSaved={load} onError={setError} />
          ))}
          {queue.length === 0 && <li className="py-3 text-sm text-neutral-500">Nothing to label{includeLabeled ? "" : " (all labeled, or sync first)"}.</li>}
        </ul>
      </Card>
    </div>
  );
}

function tri(v: boolean | null | undefined): string {
  return v === null || v === undefined ? "" : v ? "y" : "n";
}
function triParse(s: string): boolean | null {
  return s === "" ? null : s === "y";
}

function LabelRow({ item, accounts, onSaved, onError }: { item: LabelQueueItem; accounts: Account[]; onSaved: () => void; onError: (e: string) => void }) {
  const t = item.thread;
  const l = item.label;
  const [cat, setCat] = useState(l?.category ?? "");
  const [action, setAction] = useState(tri(l?.requiresAction));
  const [urg, setUrg] = useState(String(l?.urgency ?? -1));
  const [rel, setRel] = useState(String(l?.relevance ?? -1));
  const [prio, setPrio] = useState(String(l?.priority ?? -1));
  const [resolved, setResolved] = useState(tri(l?.resolved));
  const [noise, setNoise] = useState(tri(l?.noise));
  const [note, setNote] = useState(l?.note ?? "");
  const [saved, setSaved] = useState(false);
  const forge = accounts.find((a) => a.id === t.accountId)?.forge ?? "github";
  const sel = "rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900";
  const save = () => {
    const label: Label = {
      accountId: t.accountId,
      threadId: t.threadId,
      category: cat,
      requiresAction: triParse(action),
      urgency: Number(urg),
      relevance: Number(rel),
      priority: Number(prio),
      resolved: triParse(resolved),
      noise: triParse(noise),
      note,
      updatedAt: "",
    };
    EvalService.SetLabel(label)
      .then(() => {
        setSaved(true);
        onSaved();
      })
      .catch((e) => onError(errMsg(e)));
  };
  const a = item.answers ?? {};
  return (
    <li className="space-y-1 py-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        {forge === "gitlab" && <Chip tone="amber">GitLab</Chip>}
        <button type="button" className="truncate text-left font-medium hover:underline" onClick={() => openURL(t.htmlUrl)}>
          {t.title}
        </button>
        <span className="font-mono text-xs text-neutral-500">
          {t.repo}
          {t.subjectNumber ? `#${t.subjectNumber}` : ""}
        </span>
        <Chip>{t.activityKind}</Chip>
        <span className="text-xs text-neutral-500">
          {t.reason} · {timeAgo(t.updatedAt)} · {item.score.percent}%
        </span>
        {item.score.judged && (
          <span className="text-xs text-neutral-500">
            judged: {a.category?.choice} ({(a.category?.confidence ?? 0).toFixed(2)}), action {(100 * (a.requires_action_from_me?.noul ?? 0)).toFixed(0)}%, urgency {(a.urgency?.score ?? 0).toFixed(1)}, resolved{" "}
            {(100 * (a.resolved?.noul ?? 0)).toFixed(0)}%
          </span>
        )}
        {l && <Chip tone="green">labeled</Chip>}
      </div>
      {t.latestBody && <p className="line-clamp-2 text-xs text-neutral-500">{t.latestAuthor || t.actor}: {t.latestBody}</p>}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <select className={sel} value={cat} onChange={(e) => setCat(e.target.value)} title="category">
          <option value="">category…</option>
          {CATEGORIES.map((c) => (
            <option key={c} value={c}>
              {c.replace(/_/g, " ")}
            </option>
          ))}
        </select>
        <label>
          action{" "}
          <select className={sel} value={action} onChange={(e) => setAction(e.target.value)}>
            <option value="">?</option>
            <option value="y">yes</option>
            <option value="n">no</option>
          </select>
        </label>
        <label>
          urgency{" "}
          <select className={sel} value={urg} onChange={(e) => setUrg(e.target.value)}>
            <option value="-1">?</option>
            <option value="0">0 none</option>
            <option value="1">1 this week</option>
            <option value="2">2 today</option>
            <option value="3">3 blocking</option>
          </select>
        </label>
        <label>
          relevance{" "}
          <select className={sel} value={rel} onChange={(e) => setRel(e.target.value)}>
            <option value="-1">?</option>
            <option value="0">0 unrelated</option>
            <option value="1">1 tangential</option>
            <option value="2">2 my area</option>
          </select>
        </label>
        <label>
          priority{" "}
          <select className={sel} value={prio} onChange={(e) => setPrio(e.target.value)}>
            <option value="-1">?</option>
            <option value="0">0 ignore</option>
            <option value="1">1 low</option>
            <option value="2">2 medium</option>
            <option value="3">3 top</option>
          </select>
        </label>
        <label>
          resolved{" "}
          <select className={sel} value={resolved} onChange={(e) => setResolved(e.target.value)}>
            <option value="">?</option>
            <option value="y">yes</option>
            <option value="n">no</option>
          </select>
        </label>
        <label>
          noise{" "}
          <select className={sel} value={noise} onChange={(e) => setNoise(e.target.value)}>
            <option value="">?</option>
            <option value="y">yes</option>
            <option value="n">no</option>
          </select>
        </label>
        <input className={"w-40 " + sel} placeholder="note" value={note} onChange={(e) => setNote(e.target.value)} />
        <Button kind="primary" onClick={save}>
          {saved ? "Saved ✓" : "Save label"}
        </Button>
      </div>
    </li>
  );
}

function ReportTable({ r }: { r: Report }) {
  const pct = (v: number) => `${(100 * v).toFixed(0)}%`;
  return (
    <div className="mt-2 text-xs">
      <div className="mb-1 font-medium">
        {r.provider} {r.model} — {r.judged}/{r.samples} judged
      </div>
      <table className="w-full text-left">
        <tbody>
          <tr>
            <td className="pr-2 text-neutral-500">Category</td>
            <td>
              accuracy {pct(r.category.accuracy)} (n={r.category.n}); sure {pct(r.category.accSure)}, unsure {pct(r.category.accUnsure)} ({r.category.unsure} unsure)
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Needs action</td>
            <td>
              P {r.requiresAction.precision.toFixed(2)} R {r.requiresAction.recall.toFixed(2)} F1 {r.requiresAction.f1.toFixed(2)} · Brier {r.requiresAction.brier.toFixed(3)} (n={r.requiresAction.n})
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Resolved</td>
            <td>
              P {r.resolved.precision.toFixed(2)} R {r.resolved.recall.toFixed(2)} F1 {r.resolved.f1.toFixed(2)} · Brier {r.resolved.brier.toFixed(3)} (n={r.resolved.n})
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Urgency</td>
            <td>
              exact {pct(r.urgency.exact)} · ±1 {pct(r.urgency.withinOne)} · MAE {r.urgency.mae.toFixed(2)} (n={r.urgency.n})
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Relevance</td>
            <td>
              exact {pct(r.relevance.exact)} · ±1 {pct(r.relevance.withinOne)} · MAE {r.relevance.mae.toFixed(2)} (n={r.relevance.n})
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Ranking</td>
            <td>
              NDCG@10 {r.ranking.ndcg10.toFixed(3)} · @25 {r.ranking.ndcg25.toFixed(3)} · full {r.ranking.ndcg.toFixed(3)} · Spearman {r.ranking.spearman.toFixed(2)} (n={r.ranking.n}) · needs-me bucket F1 {r.needsMeBucket.f1.toFixed(2)}
            </td>
          </tr>
          <tr>
            <td className="pr-2 text-neutral-500">Filter</td>
            <td>
              P {r.filter.precision.toFixed(2)} R {r.filter.recall.toFixed(2)} F1 {r.filter.f1.toFixed(2)} (n={r.filter.n}); missed {r.filterMissed?.length ?? 0}
            </td>
          </tr>
        </tbody>
      </table>
      {(r.category.mistakes?.length ?? 0) > 0 && (
        <details className="mt-1">
          <summary className="cursor-pointer text-neutral-500">{r.category.mistakes?.length} category mistakes</summary>
          <ul className="pl-4">
            {r.category.mistakes?.map((m) => (
              <li key={m.key}>
                {m.title}: labeled <b>{m.label}</b>, predicted {m.predicted} ({m.confidence.toFixed(2)})
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
