import { useEffect, useState } from "react";
import { DiagnosticsService, MCPServerInfo } from "../bindings/ghinbox/internal/services";

// M0 shell: navigation placeholders for the planned views plus a live
// Diagnostics card proving the Go backend and the bundled MCP server resolve.
const VIEWS = ["Inbox", "Mine", "Impact", "Digest", "Profiles", "Settings", "Diagnostics"] as const;

function App() {
  const [mcp, setMcp] = useState<MCPServerInfo | null>(null);
  const [error, setError] = useState<string>("");

  useEffect(() => {
    DiagnosticsService.MCPServerInfo()
      .then(setMcp)
      .catch((e: unknown) => setError(String(e)));
  }, []);

  return (
    <div className="flex min-h-screen">
      <nav className="w-48 shrink-0 border-r border-neutral-200 p-4 dark:border-neutral-800">
        <h1 className="mb-6 text-lg font-semibold tracking-tight">GH Inbox</h1>
        <ul className="space-y-1">
          {VIEWS.map((v) => (
            <li
              key={v}
              className={
                "rounded px-2 py-1 text-sm " +
                (v === "Diagnostics"
                  ? "bg-neutral-200 font-medium dark:bg-neutral-800"
                  : "text-neutral-500 dark:text-neutral-400")
              }
            >
              {v}
            </li>
          ))}
        </ul>
      </nav>

      <main className="flex-1 p-8">
        <h2 className="mb-4 text-xl font-semibold">Diagnostics</h2>
        <section className="max-w-xl rounded-lg border border-neutral-200 bg-white p-4 dark:border-neutral-800 dark:bg-neutral-900">
          <h3 className="mb-2 text-sm font-medium uppercase tracking-wide text-neutral-500">
            GitHub MCP server
          </h3>
          {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
          {!error && !mcp && <p className="text-sm text-neutral-500">Locating…</p>}
          {mcp && mcp.error && (
            <p className="text-sm text-red-600 dark:text-red-400">{mcp.error}</p>
          )}
          {mcp && !mcp.error && (
            <dl className="grid grid-cols-[6rem_1fr] gap-y-1 text-sm">
              <dt className="text-neutral-500">Source</dt>
              <dd>{mcp.source}</dd>
              <dt className="text-neutral-500">Version</dt>
              <dd>{mcp.version || "unknown"}</dd>
              <dt className="text-neutral-500">Path</dt>
              <dd className="break-all font-mono text-xs">{mcp.path}</dd>
            </dl>
          )}
        </section>
      </main>
    </div>
  );
}

export default App;
