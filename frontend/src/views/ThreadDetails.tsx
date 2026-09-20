import { useCallback, useEffect, useState } from "react";
import { Events } from "@wailsio/runtime";
import { AgentService, ImpactService, ProseService } from "../../bindings/gitinbox/internal/services";
import type { ImpactView, SummaryView } from "../../bindings/gitinbox/internal/pipeline";
import type { Draft, Thread } from "../../bindings/gitinbox/internal/store";
import { fmtDateTime } from "../lib/format";
import { Button, ErrorText, errMsg } from "../lib/ui";
import { ImpactDetails, levelName } from "./Impact";

// ThreadDetails is the expandable fieldset under an inbox row: the full
// summary (as the summariser wrote it) and, for PRs/MRs, the same impact
// analysis block the Impact tab shows.
export default function ThreadDetails({ thread, onChanged }: { thread: Thread; onChanged: () => void }) {
  const t = thread;
  const isPR = (t.subjectType === "PullRequest" || t.subjectType === "MergeRequest") && t.subjectNumber > 0;
  const [summary, setSummary] = useState<SummaryView | null>(null);
  const [impact, setImpact] = useState<ImpactView | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [copied, setCopied] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(() => {
    const ps: Promise<unknown>[] = [
      ProseService.Summary(t.accountId, t.threadId)
        .then((s) => setSummary(s))
        .catch(() => setSummary(null)),
      AgentService.Draft(t.accountId, t.threadId)
        .then((d) => setDraft(d))
        .catch(() => setDraft(null)),
    ];
    if (isPR) {
      ps.push(
        ImpactService.Get(t.accountId, t.repo, t.subjectNumber)
          .then((v) => setImpact(v))
          .catch((e) => setError(errMsg(e))),
      );
    }
    Promise.all(ps).finally(() => setLoading(false));
  }, [t.accountId, t.threadId, t.repo, t.subjectNumber, isPR]);
  useEffect(() => {
    load();
    const off = Events.On("draft:saved", (ev: { data?: { accountId?: number; threadId?: string } }) => {
      if (ev.data?.accountId === t.accountId && ev.data?.threadId === t.threadId) load();
    });
    return () => {
      off();
    };
  }, [load, t.accountId, t.threadId]);

  const summarise = () => {
    setBusy("summary");
    setError("");
    ProseService.Summarize(t.accountId, t.threadId, !!summary)
      .then((s) => {
        setSummary(s);
        onChanged();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(""));
  };
  const analyze = () => {
    setBusy("impact");
    setError("");
    ImpactService.Analyze(t.accountId, t.repo, t.subjectNumber, "", false)
      .then(() => {
        load();
        onChanged();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(""));
  };

  return (
    <fieldset className="mt-2 rounded border border-neutral-200 bg-neutral-50 p-3 text-xs dark:border-neutral-800 dark:bg-neutral-950">
      <legend className="px-1 text-[11px] uppercase tracking-wide text-neutral-500">Details</legend>
      {error && <ErrorText>{error}</ErrorText>}
      {loading && <p className="text-neutral-500">Loading…</p>}

      <section className="mb-3">
        <div className="mb-1 flex items-center gap-2">
          <span className="font-medium">Summary</span>
          {summary && (
            <span className="text-neutral-500">
              {summary.summary.model} · {fmtDateTime(summary.summary.createdAt)}
              {summary.stale && <span className="text-amber-700"> · predates the latest activity</span>}
            </span>
          )}
          <span className="ml-auto">
            <Button kind="primary" onClick={summarise} disabled={busy !== ""} title="Ollama summary (a few seconds with a resident model)">
              {busy === "summary" ? "Summarising…" : summary ? "Re-summarise" : "Summarise"}
            </Button>
          </span>
        </div>
        {!summary && !loading && <p className="text-neutral-500">No summary yet.</p>}
        {summary && (
          <div className="space-y-1">
            <p>{summary.content.summary}</p>
            {(summary.content.key_points?.length ?? 0) > 0 && (
              <ul className="list-disc pl-5">
                {summary.content.key_points?.map((k, i) => (
                  <li key={i}>{k}</li>
                ))}
              </ul>
            )}
            {(summary.content.asks_of_me?.length ?? 0) > 0 && (
              <p>
                <b>Asks of me:</b> {summary.content.asks_of_me?.join("; ")}
              </p>
            )}
            {summary.content.changed_since_last_read && (
              <p>
                <b>Changed since last read:</b> {summary.content.changed_since_last_read}
              </p>
            )}
          </div>
        )}
      </section>

      {draft && (
        <section className="mb-3">
          <div className="mb-1 flex items-center gap-2">
            <span className="font-medium">Draft reply (from the agent, not posted)</span>
            <span className="text-neutral-500">
              {draft.model && `${draft.model} · `}
              {fmtDateTime(draft.createdAt)}
            </span>
            <span className="ml-auto flex gap-1">
              <Button
                onClick={() => {
                  navigator.clipboard?.writeText(draft.text).then(
                    () => setCopied(true),
                    () => setCopied(false),
                  );
                }}
                title="Copy the draft to the clipboard"
              >
                {copied ? "Copied" : "Copy"}
              </Button>
              <Button
                kind="ghost"
                onClick={() =>
                  AgentService.DeleteDraft(t.accountId, t.threadId)
                    .then(() => setDraft(null))
                    .catch((e) => setError(errMsg(e)))
                }
              >
                Delete
              </Button>
            </span>
          </div>
          <pre className="whitespace-pre-wrap rounded bg-white p-2 font-sans text-xs dark:bg-neutral-900">{draft.text}</pre>
        </section>
      )}

      {isPR && (
        <section>
          <div className="mb-1 flex items-center gap-2">
            <span className="font-medium">Impact analysis</span>
            {impact && (
              <span className="text-neutral-500">
                {levelName(impact.analysis.impactLevel)} · {impact.analysis.changeKind.replace(/_/g, " ")} · profile {impact.analysis.profileId}
              </span>
            )}
            {!impact && (
              <span className="ml-auto">
                <Button kind="primary" onClick={analyze} disabled={busy !== ""} title="Fetch the changed files and judge the downstream impact">
                  {busy === "impact" ? "Analysing…" : "Analyze"}
                </Button>
              </span>
            )}
          </div>
          {!impact && !loading && <p className="text-neutral-500">Not analysed yet.</p>}
          {impact && (
            <ImpactDetails
              v={impact}
              onChanged={() => {
                load();
                onChanged();
              }}
            />
          )}
        </section>
      )}
    </fieldset>
  );
}
