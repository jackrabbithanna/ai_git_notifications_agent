import { useCallback, useEffect, useState } from "react";
import { InboxService, JudgeService } from "../../bindings/ghinbox/internal/services";
import type { Scored, JudgeReport } from "../../bindings/ghinbox/internal/pipeline";
import Explain from "./Explain";
import type { InboxQuery, InboxView } from "../../bindings/ghinbox/internal/services";
import type { Account } from "../../bindings/ghinbox/internal/store";
import type { SyncReport } from "../../bindings/ghinbox/internal/pipeline";
import { openURL } from "../lib/browser";
import { fmtDuration, reasonLabel, timeAgo } from "../lib/format";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

type Props = { refreshKey: number; accountId: number; accounts: Account[] };

export default function Inbox({ refreshKey, accountId, accounts }: Props) {
  const forgeOf = (id: number) => accounts.find((a) => a.id === id)?.forge ?? "github";
  const [view, setView] = useState<InboxView | null>(null);
  const [includeRead, setIncludeRead] = useState(false);
  const [includeNoise, setIncludeNoise] = useState(false);
  const [includeDone, setIncludeDone] = useState(false);
  const [error, setError] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [reports, setReports] = useState<SyncReport[]>([]);
  const [notice, setNotice] = useState("");
  const [judging, setJudging] = useState(false);
  const [judgeReports, setJudgeReports] = useState<JudgeReport[]>([]);
  const [openExplain, setOpenExplain] = useState<string>("");

  const judge = () => {
    setJudging(true);
    setError("");
    JudgeService.Run(0, 0)
      .then((r) => {
        setJudgeReports(r ?? []);
        load();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setJudging(false));
  };

  const load = useCallback(() => {
    const q: InboxQuery = { accountId, includeRead, includeNoise, includeDone, repo: "", limit: 500 };
    InboxService.List(q)
      .then((v) => {
        setView(v);
        setError("");
      })
      .catch((e) => setError(errMsg(e)));
  }, [accountId, includeRead, includeNoise, includeDone]);

  useEffect(() => {
    load();
  }, [load, refreshKey]);

  const sync = (full: boolean) => {
    setSyncing(true);
    setError("");
    InboxService.Sync(full)
      .then((r) => setReports(r ?? []))
      .catch((e) => setError(errMsg(e)))
      .finally(() => setSyncing(false));
  };

  const act = (label: string, p: Promise<unknown>) => {
    p.then((res) => {
      const r = res as { mirrored?: boolean; warning?: string } | undefined;
      if (r && r.warning) setNotice(`${label}: ${r.warning}`);
      else setNotice("");
      load();
    }).catch((e) => setError(errMsg(e)));
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button kind="primary" onClick={() => sync(false)} disabled={syncing}>
          {syncing ? "Syncing…" : "Sync now"}
        </Button>
        <Button onClick={() => sync(true)} disabled={syncing} title="Re-read the last 7 days including read notifications">
          Full sync
        </Button>
        <Button onClick={judge} disabled={judging} title="Enrich and judge unread threads that have no fresh judgment">
          {judging ? "Judging…" : "Judge pending"}
        </Button>
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={includeRead} onChange={(e) => setIncludeRead(e.target.checked)} /> read
        </label>
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={includeNoise} onChange={(e) => setIncludeNoise(e.target.checked)} /> noise
        </label>
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={includeDone} onChange={(e) => setIncludeDone(e.target.checked)} /> done
        </label>
        {view && (
          <span className="ml-auto text-xs text-neutral-500">
            {view.total} shown · judged {view.judged} · unjudged {view.unjudged} · unread {view.counts.unread} · noise {view.counts.noise} · done{" "}
            {view.counts.done}
          </span>
        )}
      </div>

      {error && <ErrorText>{error}</ErrorText>}
      {notice && <p className="text-xs text-amber-700 dark:text-amber-300">{notice}</p>}
      {reports.length > 0 && (
        <ul className="text-xs text-neutral-600 dark:text-neutral-400">
          {reports.map((r) => (
            <li key={r.accountId}>
              {r.login}: {r.full ? "full" : "incremental"} sync fetched {r.fetched} (new {r.new}, updated {r.updated}, noise {r.noise})
              {r.mineSynced ? `; mine ${r.mineItems}` : ""} in {fmtDuration(r.duration as unknown as number)}
              {r.error && <span className="text-red-600"> — {r.error}</span>}
            </li>
          ))}
        </ul>
      )}

      {judgeReports.length > 0 && (
        <ul className="text-xs text-neutral-600 dark:text-neutral-400">
          {judgeReports.map((r) => (
            <li key={r.accountId}>
              {r.login}: {r.provider || "no provider"} {r.model} — candidates {r.candidates}, judged {r.judged}, enriched {r.enriched}, failed {r.failed}, tokens{" "}
              {r.usage.inputTokens}/{r.usage.outputTokens}
              {r.error && <span className="text-red-600"> — {r.error}</span>}
            </li>
          ))}
        </ul>
      )}

      {view && view.total === 0 && (
        <p className="text-sm text-neutral-500">Nothing to show. Add an account in Settings, then Sync.</p>
      )}

      {view?.groups?.map((g) => (
        <section key={g.kind}>
          <h3 className="mb-1 mt-4 text-xs font-semibold uppercase tracking-wide text-neutral-500">
            {g.label} <span className="font-normal">({g.threads?.length ?? 0})</span>
          </h3>
          <ul className="divide-y divide-neutral-200 rounded-lg border border-neutral-200 bg-white dark:divide-neutral-800 dark:border-neutral-800 dark:bg-neutral-900">
            {g.threads?.map((sc) => (
              <ThreadRow
                key={`${sc.thread.accountId}:${sc.thread.threadId}`}
                sc={sc}
                act={act}
                forge={forgeOf(sc.thread.accountId)}
                open={openExplain === `${sc.thread.accountId}:${sc.thread.threadId}`}
                onToggle={() => setOpenExplain((cur) => (cur === `${sc.thread.accountId}:${sc.thread.threadId}` ? "" : `${sc.thread.accountId}:${sc.thread.threadId}`))}
                onChanged={load}
              />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function ThreadRow({
  sc,
  act,
  forge,
  open,
  onToggle,
  onChanged,
}: {
  sc: Scored;
  act: (label: string, p: Promise<unknown>) => void;
  forge: string;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const t = sc.thread;
  const s = sc.score;
  const read = !t.unread || !!t.localReadAt;
  const numPrefix = t.subjectType === "MergeRequest" ? "!" : "#";
  return (
    <li className={`flex flex-wrap items-start gap-3 px-3 py-2 ${read ? "opacity-70" : ""}`}>
      <div className="w-10 shrink-0 pt-0.5 text-right" title={s.judged ? `priority ${s.priority.toFixed(2)}` : "not judged yet (prior only)"}>
        <span className={`text-xs font-semibold ${s.judged ? "" : "text-neutral-400"}`}>{s.percent}%</span>
        <span className="mt-0.5 block h-1 rounded bg-neutral-200 dark:bg-neutral-800">
          <span className={`block h-1 rounded ${s.pinned ? "bg-red-500" : s.judged ? "bg-blue-500" : "bg-neutral-400"}`} style={{ width: `${s.percent}%` }} />
        </span>
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          {s.pinned && <Chip tone="red">blocking</Chip>}
          {s.category && <Chip tone={s.bucket === "needs_me" ? "blue" : "neutral"}>{s.category.replace(/_/g, " ")}</Chip>}
          {s.unsure && <Chip tone="amber">unsure</Chip>}
          <button
            type="button"
            className="truncate text-left text-sm font-medium hover:underline"
            onClick={() => openURL(t.htmlUrl)}
            title={t.htmlUrl}
          >
            {t.title}
          </button>
          {t.filterVerdict === "noise" && <Chip tone="amber">noise: {t.filterReason}</Chip>}
          {t.doneAt && <Chip tone="green">done</Chip>}
          {t.snoozedUntil && <Chip tone="blue">snoozed</Chip>}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-neutral-500">
          {forge === "gitlab" && <Chip tone="amber">GitLab</Chip>}
          <span className="font-mono">
            {t.repo}
            {t.subjectNumber ? `${numPrefix}${t.subjectNumber}` : ""}
          </span>
          {t.actor && <span>by {t.actor}</span>}
          <Chip>{reasonLabel(t.reason)}</Chip>
          {t.relationTags?.map((r) => (
            <Chip key={r} tone="blue">
              {r}
            </Chip>
          ))}
          <span>{timeAgo(t.updatedAt)}</span>
        </div>
      </div>
      <div className="flex shrink-0 gap-1">
        {!read && (
          <Button kind="ghost" onClick={() => act("read", InboxService.MarkRead(t.accountId, t.threadId))}>
            Read
          </Button>
        )}
        {t.doneAt ? (
          <Button kind="ghost" onClick={() => act("undo", InboxService.UndoDone(t.accountId, t.threadId))}>
            Undo
          </Button>
        ) : (
          <Button kind="ghost" onClick={() => act("done", InboxService.MarkDone(t.accountId, t.threadId))}>
            Done
          </Button>
        )}
        {t.snoozedUntil ? (
          <Button kind="ghost" onClick={() => act("unsnooze", InboxService.Unsnooze(t.accountId, t.threadId))}>
            Unsnooze
          </Button>
        ) : (
          <Button kind="ghost" onClick={() => act("snooze", InboxService.Snooze(t.accountId, t.threadId, 24))} title="Hide for 24h">
            Snooze
          </Button>
        )}
        <Button
          kind="ghost"
          onClick={() => act("unsubscribe", InboxService.Unsubscribe(t.accountId, t.threadId))}
          title="Ignore this thread (mirrored to GitHub only in write mode 'notifications')"
        >
          Mute
        </Button>
        <Button kind="ghost" onClick={onToggle} title="Why this score? Answers, probabilities and the state the judge saw">
          {open ? "Hide" : "Why"}
        </Button>
      </div>
      {open && (
        <div className="basis-full">
          <Explain accountId={t.accountId} threadId={t.threadId} onChanged={onChanged} />
        </div>
      )}
    </li>
  );
}
