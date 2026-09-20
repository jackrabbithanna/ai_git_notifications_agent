import { useCallback, useEffect, useState } from "react";
import { ProseService } from "../../bindings/ghinbox/internal/services";
import type { DigestView } from "../../bindings/ghinbox/internal/pipeline";
import { fmtDateTime, fmtDuration } from "../lib/format";
import { Button, Card, ErrorText, errMsg } from "../lib/ui";

export default function Digest({ refreshKey }: { refreshKey: number }) {
  const [digests, setDigests] = useState<DigestView[]>([]);
  const [selected, setSelected] = useState(0);
  const [hours, setHours] = useState(24);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    ProseService.Digests(20)
      .then((d) => setDigests(d ?? []))
      .catch((e) => setError(errMsg(e)));
  }, []);
  useEffect(() => {
    load();
  }, [load, refreshKey]);

  const generate = () => {
    setBusy(true);
    setError("");
    ProseService.GenerateDigest(hours)
      .then(() => {
        setSelected(0);
        load();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };

  const d = digests[selected];
  return (
    <div className="max-w-4xl space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <label className="text-xs">
          period (hours){" "}
          <input type="number" className="w-20 rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={hours} onChange={(e) => setHours(Number(e.target.value))} />
        </label>
        <Button kind="primary" onClick={generate} disabled={busy} title="Generates with the Ollama digest/summary model; can take a few minutes">
          {busy ? "Generating…" : "Generate digest"}
        </Button>
        {digests.length > 1 && (
          <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={selected} onChange={(e) => setSelected(Number(e.target.value))}>
            {digests.map((x, i) => (
              <option key={x.digest.id} value={i}>
                #{x.digest.id} · {fmtDateTime(x.digest.periodEnd)} · {x.digest.threadCount} threads
              </option>
            ))}
          </select>
        )}
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {!d && <p className="text-sm text-neutral-500">No digest yet. Generate one for the last day (needs an Ollama model in Settings → Prose or Triage judge).</p>}
      {d && (
        <Card title={`Digest #${d.digest.id} · ${fmtDateTime(d.digest.periodStart)} → ${fmtDateTime(d.digest.periodEnd)}`}>
          <p className="mb-3 text-base font-medium">{d.content.headline}</p>
          {d.content.sections?.map((sec) => (
            <section key={sec.title} className="mb-3">
              <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-neutral-500">{sec.title}</h4>
              <ul className="space-y-1 text-sm">
                {sec.items?.map((it, i) => (
                  <li key={i}>
                    <span className="font-mono text-xs text-neutral-500">{it.ref}</span> <span className="font-medium">{it.title}</span> — {it.why}
                  </li>
                ))}
              </ul>
            </section>
          ))}
          {(d.content.suggested_actions?.length ?? 0) > 0 && (
            <section>
              <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-neutral-500">Suggested actions</h4>
              <ul className="list-disc space-y-0.5 pl-5 text-sm">
                {d.content.suggested_actions?.map((a, i) => (
                  <li key={i}>{a}</li>
                ))}
              </ul>
            </section>
          )}
          <p className="mt-3 text-xs text-neutral-500">
            {d.digest.model} · {d.digest.threadCount} threads · {fmtDuration(d.digest.latencyMs * 1e6)}
          </p>
        </Card>
      )}
    </div>
  );
}
