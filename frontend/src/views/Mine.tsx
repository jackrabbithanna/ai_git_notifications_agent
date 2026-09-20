import { useCallback, useEffect, useState } from "react";
import { MineService } from "../../bindings/gitinbox/internal/services";
import type { Account, Item } from "../../bindings/gitinbox/internal/store";
import { openURL } from "../lib/browser";
import { timeAgo } from "../lib/format";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

const RELATIONS = ["assigned", "mentioned", "review_requested", "author"] as const;

export default function Mine({ refreshKey, accountId, accounts }: { refreshKey: number; accountId: number; accounts: Account[] }) {
  const forgeOf = (id: number) => accounts.find((a) => a.id === id)?.forge ?? "github";
  const [items, setItems] = useState<Item[]>([]);
  const [includeClosed, setIncludeClosed] = useState(false);
  const [relation, setRelation] = useState<string>("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    MineService.List(accountId, includeClosed)
      .then((r) => setItems(r ?? []))
      .catch((e) => setError(errMsg(e)));
  }, [accountId, includeClosed]);

  useEffect(() => {
    load();
  }, [load, refreshKey]);

  const refresh = () => {
    if (!accountId) {
      setError("Select an account to refresh its searches");
      return;
    }
    setBusy(true);
    MineService.Refresh(accountId)
      .then(() => load())
      .catch((e) => setError(errMsg(e)))
      .finally(() => setBusy(false));
  };

  const shown = relation ? items.filter((i) => i.relations?.includes(relation)) : items;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button kind="primary" onClick={refresh} disabled={busy}>
          {busy ? "Searching…" : "Refresh searches"}
        </Button>
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={includeClosed} onChange={(e) => setIncludeClosed(e.target.checked)} /> closed
        </label>
        <div className="ml-2 flex gap-1">
          <FilterChip active={relation === ""} onClick={() => setRelation("")}>
            all
          </FilterChip>
          {RELATIONS.map((r) => (
            <FilterChip key={r} active={relation === r} onClick={() => setRelation(r)}>
              {r.replace("_", " ")}
            </FilterChip>
          ))}
        </div>
        <span className="ml-auto text-xs text-neutral-500">{shown.length} items</span>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      <ul className="divide-y divide-neutral-200 rounded-lg border border-neutral-200 bg-white dark:divide-neutral-800 dark:border-neutral-800 dark:bg-neutral-900">
        {shown.map((i) => (
          <li key={`${i.accountId}:${i.repo}#${i.number}`} className="flex items-start gap-3 px-3 py-2">
            <Chip tone={i.kind === "issue" ? "neutral" : "blue"}>
              {i.kind === "issue" ? "issue" : `${i.draft ? "draft " : ""}${i.kind === "mr" ? "MR" : "PR"}`}
            </Chip>
            <div className="min-w-0 flex-1">
              <button type="button" className="truncate text-left text-sm font-medium hover:underline" onClick={() => openURL(i.htmlUrl)}>
                {i.title}
              </button>
              <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-neutral-500">
                {forgeOf(i.accountId) === "gitlab" && <Chip tone="amber">GitLab</Chip>}
                <span className="font-mono">
                  {i.repo}
                  {i.kind === "mr" ? "!" : "#"}
                  {i.number}
                </span>
                <span>by {i.author}</span>
                {i.relations?.map((r) => (
                  <Chip key={r} tone="green">
                    {r.replace("_", " ")}
                  </Chip>
                ))}
                {i.labels?.slice(0, 4).map((l) => (
                  <Chip key={l}>{l}</Chip>
                ))}
                <span>{i.state}</span>
                <span>{timeAgo(i.updatedAt)}</span>
              </div>
            </div>
          </li>
        ))}
        {shown.length === 0 && <li className="px-3 py-4 text-sm text-neutral-500">No items. Sync an account first.</li>}
      </ul>
    </div>
  );
}

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`rounded-full px-2 py-0.5 text-xs ${active ? "bg-neutral-800 text-white dark:bg-neutral-200 dark:text-neutral-900" : "bg-neutral-100 text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300"}`}
    >
      {children}
    </button>
  );
}
