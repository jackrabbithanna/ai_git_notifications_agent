import { useCallback, useEffect, useState } from "react";
import { ImpactService, InboxService, JudgeService } from "../../bindings/gitinbox/internal/services";
import { levelName } from "./Impact";
import type { Scored, JudgeReport } from "../../bindings/gitinbox/internal/pipeline";
import Explain from "./Explain";
import ThreadDetails from "./ThreadDetails";
import type { InboxQuery, InboxView } from "../../bindings/gitinbox/internal/services";
import type { Account } from "../../bindings/gitinbox/internal/store";
import type { SyncReport } from "../../bindings/gitinbox/internal/pipeline";
import { openURL } from "../lib/browser";
import { fmtDuration, reasonLabel, timeAgo } from "../lib/format";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

type Props = { refreshKey: number; accountId: number; accounts: Account[] };

type Row = { sc: Scored; group: string; groupLabel: string; tags: Set<string> };

const SORTS = [
  { id: "newest", label: "Newest first" },
  { id: "priority", label: "Rated priority" },
  { id: "oldest", label: "Oldest first" },
];

// Short tab names for the server's group kinds (full label shown as tooltip).
const TAB_LABELS: Record<string, string> = {
  needs_me: "Needs me",
  review_requested: "Review requested",
  mention: "Mentions",
  assignment: "Assigned",
  new_pr: "PRs new/pushed",
  new_issue: "Issues",
  review: "Reviews",
  comment: "Replies & comments",
  state_change: "Merged/closed",
  ci: "CI",
  security: "Security",
  release: "Releases",
  discussion: "Discussions",
  commit: "Commits",
  other: "Other",
  resolved: "Resolved",
};

const CATEGORIES = ["needs_my_review", "needs_my_reply", "blocking_or_failing", "awaiting_others", "fyi_progress", "release_or_announcement", "resolved_no_action"];

// Every chip a row can show, as filterable tags. Ids are "<group>:<value>".
const TAG_GROUPS: { id: string; label: string; tags: { id: string; label: string }[] }[] = [
  {
    id: "score",
    label: "Score",
    tags: [
      { id: "score:blocking", label: "blocking" },
      { id: "score:unsure", label: "unsure" },
      { id: "score:judged", label: "judged" },
      { id: "score:unjudged", label: "not judged" },
    ],
  },
  { id: "cat", label: "Judged category", tags: CATEGORIES.map((c) => ({ id: "cat:" + c, label: c.replace(/_/g, " ") })) },
  { id: "impact", label: "Impact", tags: ["none", "possible", "likely", "certain"].map((l) => ({ id: "impact:" + l, label: "impact: " + l })) },
  {
    id: "item",
    label: "Item",
    tags: [
      { id: "item:new", label: "new" },
      { id: "item:updated", label: "updated" },
      { id: "item:pr", label: "pull/merge request" },
      { id: "item:issue", label: "issue" },
    ],
  },
  { id: "rel", label: "My relation", tags: ["author", "assignee", "reviewer", "mentioned", "participant", "subscriber"].map((r) => ({ id: "rel:" + r, label: r })) },
  {
    id: "forge",
    label: "Forge",
    tags: [
      { id: "forge:github", label: "GitHub" },
      { id: "forge:gitlab", label: "GitLab" },
    ],
  },
  {
    id: "state",
    label: "State",
    tags: [
      { id: "state:unread", label: "unread" },
      { id: "state:read", label: "read" },
      { id: "state:done", label: "done" },
      { id: "state:snoozed", label: "snoozed" },
      { id: "state:noise", label: "noise" },
    ],
  },
];

function tagsOf(sc: Scored, forge: string): Set<string> {
  const t = sc.thread;
  const s = sc.score;
  const tags = new Set<string>();
  tags.add("acct:" + t.accountId);
  if (s.pinned) tags.add("score:blocking");
  if (s.unsure) tags.add("score:unsure");
  tags.add(s.judged ? "score:judged" : "score:unjudged");
  if (s.category) tags.add("cat:" + s.category);
  if (s.impactLevel >= 0) tags.add("impact:" + levelName(s.impactLevel));
  const isPR = t.subjectType === "PullRequest" || t.subjectType === "MergeRequest";
  tags.add(isPR ? "item:pr" : "item:issue");
  if (t.enrichedVersion && (t.activityKind === "new_pr" || t.activityKind === "new_issue")) {
    const brandNew = !!t.itemCreatedAt && new Date(t.updatedAt).getTime() - new Date(t.itemCreatedAt).getTime() < 30 * 60 * 1000;
    tags.add(brandNew ? "item:new" : "item:updated");
  }
  t.relationTags?.forEach((r) => tags.add("rel:" + r));
  tags.add("forge:" + forge);
  const read = !t.unread || !!t.localReadAt;
  tags.add(read ? "state:read" : "state:unread");
  if (t.doneAt) tags.add("state:done");
  if (t.snoozedUntil) tags.add("state:snoozed");
  if (t.filterVerdict === "noise") tags.add("state:noise");
  return tags;
}

function readPref(key: string, def: string): string {
  try {
    return localStorage.getItem(key) ?? def;
  } catch {
    return def;
  }
}
function writePref(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* storage unavailable */
  }
}

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
  const [openDetails, setOpenDetails] = useState<string>("");
  const [activeTab, setActiveTabState] = useState<string>(() => readPref("inbox.tab", "all"));
  const [sort, setSortState] = useState<string>(() => readPref("inbox.sort", "newest"));
  const [selectedTags, setSelectedTags] = useState<Set<string>>(new Set());
  const [showFilters, setShowFilters] = useState(false);
  const setTab = (t: string) => {
    setActiveTabState(t);
    writePref("inbox.tab", t);
  };
  const setSort = (v: string) => {
    setSortState(v);
    writePref("inbox.sort", v);
  };
  const toggleTag = (id: string) =>
    setSelectedTags((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // Flatten the server's groups; tabs, filters and sort are applied client-side.
  const rows: Row[] = (view?.groups ?? []).flatMap((g) => (g.threads ?? []).map((sc) => ({ sc, group: g.kind, groupLabel: g.label, tags: tagsOf(sc, forgeOf(sc.thread.accountId)) })));
  const passesTags = (r: Row) => {
    const byGroup = new Map<string, string[]>();
    selectedTags.forEach((id) => {
      const g = id.split(":")[0];
      byGroup.set(g, [...(byGroup.get(g) ?? []), id]);
    });
    for (const ids of byGroup.values()) if (!ids.some((id) => r.tags.has(id))) return false;
    return true;
  };
  const filtered = rows.filter(passesTags);
  const tabs = [{ kind: "all", label: "All", title: "Every thread", count: filtered.length }].concat(
    (view?.groups ?? []).map((g) => ({ kind: g.kind, label: TAB_LABELS[g.kind] ?? g.label, title: g.label, count: filtered.filter((r) => r.group === g.kind).length })),
  );
  if (activeTab !== "all" && !tabs.some((t) => t.kind === activeTab) && view) {
    // The remembered tab is empty right now; fall back to All without losing the preference.
  }
  const inTab = activeTab === "all" || !tabs.some((t) => t.kind === activeTab) ? filtered : filtered.filter((r) => r.group === activeTab);
  const visible = [...inTab].sort((a, b) => {
    switch (sort) {
      case "priority":
        if (a.sc.score.pinned !== b.sc.score.pinned) return a.sc.score.pinned ? -1 : 1;
        if (a.sc.score.priority !== b.sc.score.priority) return b.sc.score.priority - a.sc.score.priority;
        return new Date(b.sc.thread.updatedAt).getTime() - new Date(a.sc.thread.updatedAt).getTime();
      case "oldest":
        return new Date(a.sc.thread.updatedAt).getTime() - new Date(b.sc.thread.updatedAt).getTime();
      default:
        return new Date(b.sc.thread.updatedAt).getTime() - new Date(a.sc.thread.updatedAt).getTime();
    }
  });
  const tagCounts = new Map<string, number>();
  rows.forEach((r) => r.tags.forEach((t) => tagCounts.set(t, (tagCounts.get(t) ?? 0) + 1)));
  const tagGroups = [
    { id: "acct", label: "Account", tags: accounts.map((a) => ({ id: "acct:" + a.id, label: `${a.login}@${a.host}` })) },
    ...TAG_GROUPS,
  ];
  const accountName = (id: number) => {
    const a = accounts.find((x) => x.id === id);
    return a ? `${a.login}@${a.host}` : "";
  };

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

  const analyze = (accountId: number, repo: string, number: number) => {
    setError("");
    setNotice(`Analysing ${repo}#${number}… (fetching files, judging)`);
    ImpactService.Analyze(accountId, repo, number, "", false)
      .then((a) => {
        setNotice(`Analysed ${repo}#${number}: impact ${levelName(a.impactLevel)} · ${a.changeKind.replace(/_/g, " ")} · profile ${a.profileId} — details in the Impact view`);
        load();
      })
      .catch((e) => {
        setNotice("");
        setError(`Analyse ${repo}#${number}: ${errMsg(e)}`);
      });
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

      {view && view.total > 0 && (
        <>
          <div className="flex flex-wrap gap-1 border-b border-neutral-200 dark:border-neutral-800">
            {tabs.map((tab) => (
              <button
                key={tab.kind}
                type="button"
                title={tab.title}
                onClick={() => setTab(tab.kind)}
                className={`-mb-px rounded-t px-3 py-1.5 text-xs ${
                  tab.kind === activeTab
                    ? "border border-b-white border-neutral-200 bg-white font-medium dark:border-neutral-700 dark:border-b-neutral-900 dark:bg-neutral-900"
                    : "text-neutral-600 hover:bg-neutral-100 dark:text-neutral-300 dark:hover:bg-neutral-800"
                }`}
              >
                {tab.label} <span className="text-neutral-400">{tab.count}</span>
              </button>
            ))}
          </div>

          <div className="flex flex-wrap items-center gap-2 text-xs">
            <label>
              sort{" "}
              <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={sort} onChange={(e) => setSort(e.target.value)}>
                {SORTS.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <Button kind={selectedTags.size > 0 ? "primary" : "default"} onClick={() => setShowFilters((v) => !v)}>
              Filters{selectedTags.size > 0 ? ` (${selectedTags.size})` : ""}
            </Button>
            {selectedTags.size > 0 && (
              <Button kind="ghost" onClick={() => setSelectedTags(new Set())}>
                clear
              </Button>
            )}
            <span className="text-neutral-500">
              {visible.length} of {rows.length}
            </span>
          </div>

          {showFilters && (
            <div className="grid gap-2 rounded-lg border border-neutral-200 bg-white p-3 text-xs md:grid-cols-2 lg:grid-cols-3 dark:border-neutral-800 dark:bg-neutral-900">
              {tagGroups.map((g) => (
                <div key={g.id}>
                  <div className="mb-1 text-[11px] uppercase tracking-wide text-neutral-500">{g.label}</div>
                  <div className="flex flex-wrap gap-1">
                    {g.tags.map((tg) => {
                      const on = selectedTags.has(tg.id);
                      const n = tagCounts.get(tg.id) ?? 0;
                      return (
                        <button
                          key={tg.id}
                          type="button"
                          onClick={() => toggleTag(tg.id)}
                          className={`rounded-full px-2 py-0.5 ${on ? "bg-neutral-800 text-white dark:bg-neutral-200 dark:text-neutral-900" : "bg-neutral-100 text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300"}`}
                        >
                          {tg.label} <span className={on ? "opacity-70" : "text-neutral-400"}>{n}</span>
                        </button>
                      );
                    })}
                  </div>
                </div>
              ))}
              <p className="text-neutral-500 md:col-span-2 lg:col-span-3">Within a group any selected tag matches; across groups all must match.</p>
            </div>
          )}

          <ul className="divide-y divide-neutral-200 rounded-lg border border-neutral-200 bg-white dark:divide-neutral-800 dark:border-neutral-800 dark:bg-neutral-900">
            {visible.map(({ sc }) => (
              <ThreadRow
                key={`${sc.thread.accountId}:${sc.thread.threadId}`}
                sc={sc}
                act={act}
                forge={forgeOf(sc.thread.accountId)}
                account={accounts.length > 1 ? accountName(sc.thread.accountId) : ""}
                open={openExplain === `${sc.thread.accountId}:${sc.thread.threadId}`}
                onToggle={() => setOpenExplain((cur) => (cur === `${sc.thread.accountId}:${sc.thread.threadId}` ? "" : `${sc.thread.accountId}:${sc.thread.threadId}`))}
                detailsOpen={openDetails === `${sc.thread.accountId}:${sc.thread.threadId}`}
                onToggleDetails={() => setOpenDetails((cur) => (cur === `${sc.thread.accountId}:${sc.thread.threadId}` ? "" : `${sc.thread.accountId}:${sc.thread.threadId}`))}
                onChanged={load}
                onAnalyze={analyze}
              />
            ))}
            {visible.length === 0 && <li className="px-3 py-4 text-sm text-neutral-500">Nothing matches this tab and filter.</li>}
          </ul>
        </>
      )}
    </div>
  );
}

function ThreadRow({
  sc,
  act,
  forge,
  account,
  open,
  onToggle,
  detailsOpen,
  onToggleDetails,
  onChanged,
  onAnalyze,
}: {
  sc: Scored;
  act: (label: string, p: Promise<unknown>) => void;
  forge: string;
  account: string;
  open: boolean;
  onToggle: () => void;
  detailsOpen: boolean;
  onToggleDetails: () => void;
  onChanged: () => void;
  onAnalyze: (accountId: number, repo: string, number: number) => void;
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
          {s.impactLevel >= 0 && (
            <span className="cursor-pointer" onClick={onToggleDetails} title={sc.impact ? `${sc.impact.changeKind.replace(/_/g, " ")} · layers: ${sc.impact.layers?.join(", ") || "none"} · ${sc.impact.surfaceHits} surface hits${sc.impact.hasNote ? " · note written" : ""} — click for details` : ""}>
              <Chip tone={s.impactLevel >= 3 ? "red" : s.impactLevel === 2 ? "amber" : s.impactLevel === 1 ? "blue" : "neutral"}>impact: {levelName(s.impactLevel)}</Chip>
            </span>
          )}
          {t.enrichedVersion && (t.activityKind === "new_pr" || t.activityKind === "new_issue") && (
            <Chip tone="neutral">{t.itemCreatedAt && new Date(t.updatedAt).getTime() - new Date(t.itemCreatedAt).getTime() < 30 * 60 * 1000 ? "new" : "updated"}</Chip>
          )}
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
        {sc.summary && (
          <p className="mt-0.5 line-clamp-2 cursor-pointer text-xs text-neutral-600 hover:underline dark:text-neutral-400" title="Open details" onClick={onToggleDetails}>
            {sc.summary}
            {sc.summaryStale && <span className="text-amber-700"> (summary predates the latest activity)</span>}
          </p>
        )}
        <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-neutral-500">
          {forge === "gitlab" && <Chip tone="amber">GitLab</Chip>}
          {account && <span className="text-neutral-400">{account}</span>}
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
        {(t.subjectType === "PullRequest" || t.subjectType === "MergeRequest") && t.subjectNumber > 0 && (
          <Button
            kind="ghost"
            onClick={() => onAnalyze(t.accountId, t.repo, t.subjectNumber)}
            title="Impact analysis against the matching profile (generic if none); result appears as an impact chip and in the Impact view"
          >
            {s.impactLevel >= 0 ? "Re-analyze" : "Analyze"}
          </Button>
        )}
        <Button kind="ghost" onClick={onToggleDetails} title="Full summary and impact analysis">
          {detailsOpen ? "Hide details" : "Details"}
        </Button>
        <Button kind="ghost" onClick={onToggle} title="Why this score? Answers, probabilities and the state the judge saw">
          {open ? "Hide" : "Why"}
        </Button>
      </div>
      {detailsOpen && (
        <div className="basis-full">
          <ThreadDetails thread={t} onChanged={onChanged} />
        </div>
      )}
      {open && (
        <div className="basis-full">
          <Explain accountId={t.accountId} threadId={t.threadId} onChanged={onChanged} />
        </div>
      )}
    </li>
  );
}
