import { useEffect, useState } from "react";
import { JudgeService } from "../../bindings/ghinbox/internal/services";
import type { Explanation } from "../../bindings/ghinbox/internal/pipeline";
import type { Answer } from "../../bindings/ghinbox/internal/judge";
import { fmtDateTime } from "../lib/format";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

// Explain panel: the judge's answers with probabilities, the state it saw, and
// the resulting score. "Judge now" re-runs enrichment + judgment for this thread.
export default function Explain({ accountId, threadId, onChanged }: { accountId: number; threadId: string; onChanged: () => void }) {
  const [ex, setEx] = useState<Explanation | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showState, setShowState] = useState(false);

  const load = (now: boolean) => {
    setBusy(true);
    setError("");
    JudgeService.Explain(accountId, threadId, now)
      .then((e) => {
        setEx(e);
        if (now) onChanged();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };
  useEffect(() => {
    load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountId, threadId]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!ex) return <p className="text-xs text-neutral-500">Loading…</p>;
  const answers = ex.answers ?? {};
  const t = ex.thread;
  return (
    <div className="mt-2 rounded border border-neutral-200 bg-neutral-50 p-3 text-xs dark:border-neutral-800 dark:bg-neutral-950">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="font-medium">Score {ex.score.percent}%</span>
        <Chip>{ex.score.bucket}</Chip>
        {ex.score.pinned && <Chip tone="red">pinned</Chip>}
        {ex.score.unsure && <Chip tone="amber">unsure</Chip>}
        {ex.judgment ? (
          <span className="text-neutral-500">
            judged by {ex.judgment.provider} {ex.judgment.model} · {ex.judgment.calibrated ? "calibrated" : "uncalibrated"} · {fmtDateTime(ex.judgment.createdAt)}
            {ex.stale && <span className="text-amber-700"> · stale (thread changed since)</span>}
          </span>
        ) : (
          <span className="text-neutral-500">not judged yet</span>
        )}
        <span className="ml-auto flex gap-1">
          <Button onClick={() => setShowState((s) => !s)}>{showState ? "Hide state" : "Show state"}</Button>
          <Button kind="primary" onClick={() => load(true)} disabled={busy}>
            {busy ? "Judging…" : "Judge now"}
          </Button>
        </span>
      </div>
      {t.enrichedVersion && (
        <div className="mb-2 text-neutral-600 dark:text-neutral-400">
          <span className="font-medium">Item</span> by {t.itemAuthor || "?"} · {t.itemState || "?"}
          {t.itemLabels && t.itemLabels.length > 0 && <> · {t.itemLabels.join(", ")}</>}
          {t.latestBody && (
            <div className="mt-1 whitespace-pre-wrap border-l-2 border-neutral-300 pl-2 dark:border-neutral-700">
              <span className="font-medium">{t.latestAuthor || t.actor}</span>: {t.latestBody.slice(0, 400)}
              {t.latestBody.length > 400 ? "…" : ""}
            </div>
          )}
        </div>
      )}
      {ex.questions && Object.keys(answers).length > 0 && (
        <table className="w-full">
          <tbody>
            {ex.questions.map((q) => {
              const a = answers[q.id];
              if (!a) return null;
              return (
                <tr key={q.id} className="border-t border-neutral-200 align-top dark:border-neutral-800">
                  <td className="py-1 pr-2 font-mono text-neutral-500">{q.id}</td>
                  <td className="py-1">{renderAnswer(a)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {showState && (
        <pre className="mt-2 max-h-72 overflow-auto rounded bg-white p-2 font-mono text-[11px] dark:bg-neutral-900">{JSON.stringify(ex.state, null, 2)}</pre>
      )}
    </div>
  );
}

function renderAnswer(a: Answer) {
  const probs = a.probabilities ?? {};
  const sorted = Object.entries(probs).sort((x, y) => (y[1] ?? 0) - (x[1] ?? 0));
  if (a.kind === "noul") {
    return (
      <span>
        <Bar value={a.noul ?? 0} /> {(100 * (a.noul ?? 0)).toFixed(0)}% yes
      </span>
    );
  }
  if (a.kind === "choice") {
    return (
      <span>
        <span className="font-medium">{a.choice}</span> <span className="text-neutral-500">(conf {(a.confidence ?? 0).toFixed(2)})</span>
        <span className="ml-2 text-neutral-500">{sorted.map(([k, v]) => `${k} ${(100 * (v ?? 0)).toFixed(0)}%`).join(" · ")}</span>
      </span>
    );
  }
  const legend = a.legend ?? [];
  const idx = Math.round(a.score ?? 0);
  return (
    <span>
      <span className="font-medium">
        {(a.score ?? 0).toFixed(2)} / {Math.max(legend.length - 1, 0)}
      </span>{" "}
      <span className="text-neutral-500">{legend[idx] ?? ""}</span>
      <span className="ml-2 text-neutral-500">(conf {(a.confidence ?? 0).toFixed(2)})</span>
    </span>
  );
}

function Bar({ value }: { value: number }) {
  return (
    <span className="mr-1 inline-block h-2 w-24 rounded bg-neutral-200 align-middle dark:bg-neutral-800">
      <span className="block h-2 rounded bg-blue-500" style={{ width: `${Math.round(100 * Math.max(0, Math.min(1, value)))}%` }} />
    </span>
  );
}
