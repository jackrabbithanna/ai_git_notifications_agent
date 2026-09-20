import { useCallback, useEffect, useState } from "react";
import { Events } from "@wailsio/runtime";
import { AccountsService } from "../bindings/ghinbox/internal/services";
import type { Account } from "../bindings/ghinbox/internal/store";
import Inbox from "./views/Inbox";
import Mine from "./views/Mine";
import Settings from "./views/Settings";
import Diagnostics from "./views/Diagnostics";

const VIEWS = ["Inbox", "Mine", "Settings", "Diagnostics"] as const;
type View = (typeof VIEWS)[number];

// Planned views not yet implemented (see PLAN.md milestones).
const PLANNED = ["Impact", "Digest", "Profiles"];

function App() {
  const [view, setView] = useState<View>("Inbox");
  const [refreshKey, setRefreshKey] = useState(0);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [accountId, setAccountId] = useState(0); // 0 = all accounts
  const [lastError, setLastError] = useState("");

  const loadAccounts = useCallback(() => {
    AccountsService.List()
      .then((a) => setAccounts((a ?? []).map((v) => v.account)))
      .catch((e) => setLastError(String(e)));
  }, []);

  useEffect(() => {
    loadAccounts();
    const offUpdated = Events.On("inbox:updated", () => setRefreshKey((k) => k + 1));
    const offErr = Events.On("sync:error", (ev: { data?: { login?: string; error?: string } }) => {
      const d = ev.data ?? {};
      setLastError(`${d.login ?? "sync"}: ${d.error ?? "failed"}`);
    });
    const offRep = Events.On("sync:report", () => setLastError(""));
    return () => {
      offUpdated();
      offErr();
      offRep();
    };
  }, [loadAccounts]);

  useEffect(() => {
    if (accounts.length === 0 && view !== "Settings" && view !== "Diagnostics") setView("Settings");
  }, [accounts.length, view]);

  return (
    <div className="flex min-h-screen">
      <nav className="flex w-48 shrink-0 flex-col border-r border-neutral-200 p-4 dark:border-neutral-800">
        <h1 className="mb-6 text-lg font-semibold tracking-tight">GH Inbox</h1>
        <ul className="space-y-1">
          {VIEWS.map((v) => (
            <li key={v}>
              <button
                type="button"
                onClick={() => setView(v)}
                className={`w-full rounded px-2 py-1 text-left text-sm ${
                  v === view ? "bg-neutral-200 font-medium dark:bg-neutral-800" : "text-neutral-600 hover:bg-neutral-100 dark:text-neutral-300 dark:hover:bg-neutral-800"
                }`}
              >
                {v}
              </button>
            </li>
          ))}
          {PLANNED.map((v) => (
            <li key={v} className="px-2 py-1 text-sm text-neutral-400 dark:text-neutral-600" title="Planned">
              {v}
            </li>
          ))}
        </ul>
        {accounts.length > 1 && (
          <label className="mt-6 block text-xs text-neutral-500">
            Account
            <select
              className="mt-1 w-full rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900"
              value={accountId}
              onChange={(e) => setAccountId(Number(e.target.value))}
            >
              <option value={0}>all accounts</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.login}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className="mt-auto pt-6 text-[11px] text-neutral-400">
          {accounts.map((a) => (
            <div key={a.id}>
              {a.login} · {a.writeMode === "readonly" ? "read-only" : "writes: notifications"}
            </div>
          ))}
        </div>
      </nav>

      <main className="min-w-0 flex-1 p-6">
        <header className="mb-4 flex items-center gap-3">
          <h2 className="text-xl font-semibold">{view}</h2>
          {lastError && <span className="truncate text-xs text-red-600 dark:text-red-400">{lastError}</span>}
        </header>
        {view === "Inbox" && <Inbox refreshKey={refreshKey} accountId={accountId} />}
        {view === "Mine" && <Mine refreshKey={refreshKey} accountId={accountId || accounts[0]?.id || 0} />}
        {view === "Settings" && <Settings onAccountsChanged={loadAccounts} />}
        {view === "Diagnostics" && <Diagnostics accounts={accounts} />}
      </main>
    </div>
  );
}

export default App;
