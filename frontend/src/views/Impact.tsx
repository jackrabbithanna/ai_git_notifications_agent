import { useCallback, useEffect, useState } from "react";
import { EvalService, ImpactService } from "../../bindings/gitinbox/internal/services";
import type { ImpactQuery } from "../../bindings/gitinbox/internal/services";
import type { ImpactReport, ImpactView } from "../../bindings/gitinbox/internal/pipeline";
import type { Account } from "../../bindings/gitinbox/internal/store";
import type { Answer } from "../../bindings/gitinbox/internal/judge";
import { openURL } from "../lib/browser";
import { fmtDateTime, timeAgo } from "../lib/format";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

const LEVELS = ["none", "possible", "likely", "certain"] as const;
const LEVEL_TONE: Record<string, "neutral" | "blue" | "amber" | "red" | "green"> = { none: "neutral", possible: "blue", likely: "amber", certain: "red" };

export function levelName(l: number): string {
  return l < 0 ? "unanalysed" : (LEVELS[l] ?? "?");
}

export default function Impact({ refreshKey, accountId, accounts }: { refreshKey: number; accountId: number; accounts: Account[] }) {
  const [landed, setLanded] = useState(false);
  const [minLevel, setMinLevel] = useState(1);
  const [rows, setRows] = useState<ImpactView[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [reports, setReports] = useState<ImpactReport[]>([]);
  const [ref, setRef] = useState("");
  const [refAccount, setRefAccount] = useState<number>(accounts[0]?.id ?? 0);
  const [open, setOpen] = useState("");

  const load = useCallback(() => {
    const q: ImpactQuery = { accountId, minLevel, landed, limit: 200 };
    ImpactService.List(q)
      .then((r) => {
        setRows(r ?? []);
        setError("");
      })
      .catch((e) => setError(errMsg(e)));
  }, [accountId, minLevel, landed]);
  useEffect(() => {
    load();
  }, [load, refreshKey]);
  useEffect(() => {
    if (!refAccount && accounts[0]) setRefAccount(accounts[0].id);
  }, [accounts, refAccount]);

  const runPending = () => {
    setBusy(true);
    ImpactService.RunPending(0)
      .then((r) => {
        setReports(r ?? []);
        load();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };
  const analyzeRef = () => {
    const m = ref.trim().match(/^(.+?)[#!](\d+)$/);
    if (!m || !refAccount) {
      setError("Enter owner/repo#123 (or group/project!12) and pick the account that can reach it");
      return;
    }
    setBusy(true);
    ImpactService.Analyze(refAccount, m[1], Number(m[2]), "", false)
      .then(() => {
        setRef("");
        setMinLevel(0);
        load();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button kind={!landed ? "primary" : "default"} onClick={() => setLanded(false)}>
          Proposed (open)
        </Button>
        <Button kind={landed ? "primary" : "default"} onClick={() => setLanded(true)}>
          Landed (merged)
        </Button>
        <label className="ml-2 text-xs">
          min level{" "}
          <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={minLevel} onChange={(e) => setMinLevel(Number(e.target.value))}>
            {LEVELS.map((l, i) => (
              <option key={l} value={i}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <Button onClick={runPending} disabled={busy} title="Analyse PR threads in profile repos without a fresh analysis; scan profile repos for recently merged PRs">
          {busy ? "Working…" : "Analyse pending"}
        </Button>
        <span className="ml-auto flex items-center gap-1">
          <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900" value={refAccount} onChange={(e) => setRefAccount(Number(e.target.value))}>
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>
                {a.login}@{a.host}
              </option>
            ))}
          </select>
          <input
            className="w-64 rounded border border-neutral-300 bg-white px-2 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900"
            placeholder="analyse any PR: owner/repo#123"
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && analyzeRef()}
          />
          <Button onClick={analyzeRef} disabled={busy || !ref.trim()}>
            Analyse
          </Button>
        </span>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {reports.length > 0 && (
        <ul className="text-xs text-neutral-600 dark:text-neutral-400">
          {reports.map((r) => (
            <li key={r.accountId}>
              {r.login}: candidates {r.candidates}, analysed {r.analysed}, skipped {r.skipped}, failed {r.failed}, landed {r.landed}
              {r.error && <span className="text-red-600"> — {r.error}</span>}
              {r.errors?.map((e) => (
                <div key={e} className="pl-3 text-red-600">
                  {e}
                </div>
              ))}
            </li>
          ))}
        </ul>
      )}
      <ul className="divide-y divide-neutral-200 rounded-lg border border-neutral-200 bg-white dark:divide-neutral-800 dark:border-neutral-800 dark:bg-neutral-900">
        {rows.map((v) => {
          const a = v.analysis;
          const key = `${a.accountId}:${a.repo}#${a.number}`;
          const num = a.kind === "mr" ? `!${a.number}` : `#${a.number}`;
          return (
            <li key={key} className="px-3 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <Chip tone={LEVEL_TONE[levelName(a.impactLevel)] ?? "neutral"}>{levelName(a.impactLevel)}</Chip>
                {a.changeKind && <Chip>{a.changeKind.replace(/_/g, " ")}</Chip>}
                <button type="button" className="truncate text-left text-sm font-medium hover:underline" onClick={() => openURL(a.htmlUrl)} title={a.htmlUrl}>
                  {a.title}
                </button>
                <span className="font-mono text-xs text-neutral-500">
                  {a.repo}
                  {num}
                </span>
                <span className="text-xs text-neutral-500">
                  {a.state} · by {a.author} · {timeAgo(a.updatedAt)}
                </span>
                {v.report.layers?.map((l) => (
                  <Chip key={l.id} tone="blue">
                    {l.label} ({l.files?.length ?? 0})
                  </Chip>
                ))}
                {(v.report.surfaceHits?.length ?? 0) > 0 && <Chip tone="amber">{v.report.surfaceHits?.length} surface hits</Chip>}
                {v.note && <Chip tone="green">note</Chip>}
                <span className="ml-auto flex gap-1">
                  <Button kind="ghost" onClick={() => setOpen((cur) => (cur === key ? "" : key))}>
                    {open === key ? "Hide" : "Details"}
                  </Button>
                </span>
              </div>
              {open === key && <ImpactDetails v={v} onChanged={load} />}
            </li>
          );
        })}
        {rows.length === 0 && (
          <li className="px-3 py-4 text-sm text-neutral-500">No analyses at this level. Run "Analyse pending" (needs a judge provider and a profile covering the repo) or analyse a PR by reference.</li>
        )}
      </ul>
    </div>
  );
}

export function ImpactDetails({ v, onChanged }: { v: ImpactView; onChanged: () => void }) {
  const a = v.analysis;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [labelLevel, setLabelLevel] = useState("-1");
  const [labelKind, setLabelKind] = useState("");
  const [labelSaved, setLabelSaved] = useState(false);
  const saveLabel = () =>
    EvalService.SetPRLabel({ accountId: a.accountId, repo: a.repo, number: a.number, impactLevel: Number(labelLevel), changeKind: labelKind, note: "", updatedAt: "" })
      .then(() => setLabelSaved(true))
      .catch((e) => setError(errMsg(e)));
  const answers = v.answers ?? {};
  const act = (p: Promise<unknown>) => {
    setBusy(true);
    setError("");
    p.then(onChanged)
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };
  return (
    <div className="mt-2 rounded border border-neutral-200 bg-neutral-50 p-3 text-xs dark:border-neutral-800 dark:bg-neutral-950">
      <div className="mb-2 flex flex-wrap items-center gap-2 text-neutral-500">
        profile <b>{a.profileId}</b> · head {a.headSha.slice(0, 10)} · judged by {a.provider} {a.model} {a.calibrated ? "(calibrated)" : "(uncalibrated)"} · {fmtDateTime(a.analysedAt)}
        {a.error && <span className="text-red-600">{a.error}</span>}
        <span className="ml-auto flex gap-1">
          <Button onClick={() => act(ImpactService.Analyze(a.accountId, a.repo, a.number, "", true))} disabled={busy}>
            Re-analyse
          </Button>
          <Button kind="primary" onClick={() => act(ImpactService.Note(a.accountId, a.repo, a.number))} disabled={busy} title="Generate a note with the Ollama model (can take minutes)">
            {v.note ? "Rewrite note" : "Write note"}
          </Button>
        </span>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
        <span className="text-neutral-500">Your label:</span>
        <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 dark:border-neutral-700 dark:bg-neutral-900" value={labelLevel} onChange={(e) => setLabelLevel(e.target.value)}>
          <option value="-1">impact?</option>
          {LEVELS.map((l, i) => (
            <option key={l} value={i}>
              {l}
            </option>
          ))}
        </select>
        <select className="rounded border border-neutral-300 bg-white px-1 py-0.5 dark:border-neutral-700 dark:bg-neutral-900" value={labelKind} onChange={(e) => setLabelKind(e.target.value)}>
          <option value="">change kind?</option>
          {["bugfix", "refactor_internal", "new_feature", "architectural", "api_change", "data_model_change", "deprecation_or_removal", "dependency_or_packaging", "docs_tests_only"].map((k) => (
            <option key={k} value={k}>
              {k.replace(/_/g, " ")}
            </option>
          ))}
        </select>
        <Button onClick={saveLabel}>{labelSaved ? "Saved ✓" : "Save PR label"}</Button>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <div className="mb-1 font-medium">Layers touched ({v.report.totalFiles} files, {v.report.ignored} ignored)</div>
          <ul>
            {v.report.layers?.map((l) => (
              <li key={l.id}>
                <b>{l.label}</b> +{l.additions}/−{l.deletions}: <span className="font-mono">{l.files?.slice(0, 6).join(", ")}</span>
                {(l.files?.length ?? 0) > 6 ? " …" : ""}
              </li>
            ))}
            {(v.report.unclassified?.length ?? 0) > 0 && <li className="text-neutral-500">unclassified: {v.report.unclassified?.slice(0, 5).join(", ")}</li>}
          </ul>
          {(v.report.signals?.length ?? 0) > 0 && (
            <div className="mt-1">
              signals: {v.report.signals?.map((s) => <Chip key={s}>{s}</Chip>)}
            </div>
          )}
          {(v.report.surfaceHits?.length ?? 0) > 0 && (
            <div className="mt-1">
              <div className="font-medium">Surface hits</div>
              <ul className="font-mono">
                {v.report.surfaceHits?.slice(0, 8).map((h, i) => (
                  <li key={i} className="truncate" title={h.line}>
                    [{h.patternId}] {h.path}: {h.line}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
        <div>
          <div className="mb-1 font-medium">Judgment</div>
          <table className="w-full">
            <tbody>
              {["change_kind", "downstream_impact", "affects_downstream", "backward_compatible", "needs_my_attention_now"].map((id) => {
                const ans = answers[id];
                if (!ans) return null;
                return (
                  <tr key={id} className="border-t border-neutral-200 align-top dark:border-neutral-800">
                    <td className="py-0.5 pr-2 font-mono text-neutral-500">{id}</td>
                    <td className="py-0.5">{renderAnswer(ans)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {v.note && (
            <div className="mt-2 space-y-1">
              <div className="font-medium">Impact note ({a.noteModel})</div>
              <p>
                <b>What changed:</b> {v.note.what_changed}
              </p>
              <p>
                <b>Why it matters:</b> {v.note.why_it_matters_for_downstream}
              </p>
              {(v.note.surfaces_changed?.length ?? 0) > 0 && (
                <p>
                  <b>Surfaces:</b> {v.note.surfaces_changed?.join("; ")}
                </p>
              )}
              {(v.note.recommended_checks?.length ?? 0) > 0 && (
                <p>
                  <b>Checks:</b> {v.note.recommended_checks?.join("; ")}
                </p>
              )}
              {v.note.migration_hints && (
                <p>
                  <b>Migration:</b> {v.note.migration_hints}
                </p>
              )}
              {v.note.confidence_note && <p className="text-neutral-500">{v.note.confidence_note}</p>}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function renderAnswer(a: Answer) {
  const probs = Object.entries(a.probabilities ?? {}).sort((x, y) => (y[1] ?? 0) - (x[1] ?? 0));
  if (a.kind === "noul") return <span>{(100 * (a.noul ?? 0)).toFixed(0)}% yes</span>;
  if (a.kind === "choice")
    return (
      <span>
        <b>{a.choice}</b> <span className="text-neutral-500">(conf {(a.confidence ?? 0).toFixed(2)}) {probs.map(([k, v]) => `${k} ${(100 * (v ?? 0)).toFixed(0)}%`).join(" · ")}</span>
      </span>
    );
  const legend = a.legend ?? [];
  return (
    <span>
      <b>{(a.score ?? 0).toFixed(2)}</b> / {Math.max(legend.length - 1, 0)} <span className="text-neutral-500">{legend[Math.round(a.score ?? 0)] ?? ""} (conf {(a.confidence ?? 0).toFixed(2)})</span>
    </span>
  );
}
