import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Events } from "@wailsio/runtime";
import { AgentService } from "../../bindings/gitinbox/internal/services";
import type { AgentStatus } from "../../bindings/gitinbox/internal/services";
import type { Message, ModelInfo } from "../../bindings/gitinbox/internal/agent";

// UIEvent mirrors agent.UIEvent (not part of the generated bindings: no service returns it).
type UIEvent = {
  type: string;
  delta?: string;
  messageId?: number;
  toolCallId?: string;
  toolName?: string;
  args?: unknown;
  result?: string;
  isError?: boolean;
  error?: string;
  busy: boolean;
};
import { openURL } from "../lib/browser";
import { Button, Chip, ErrorText, errMsg } from "../lib/ui";

// Agent view: chat with the pi sidecar. The transcript lives in Go
// (AgentService.Transcript); text deltas stream in over agent:event so the
// reply appears as it is written.

const SUGGESTIONS = [
  "What needs me most right now? Top 5 with links.",
  "Which merged pull requests could affect my extensions? Summarise the impact.",
  "List my open review requests and what each one is waiting on.",
  "What changed in the last day on the CiviCRM core threads I follow?",
];

type Prefill = { text: string; key: number } | null;

export default function Agent({ prefill }: { prefill: Prefill }) {
  const [status, setStatus] = useState<AgentStatus | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const bottomRef = useRef<HTMLDivElement>(null);
  const lastPrefill = useRef(0);

  const loadStatus = useCallback(() => {
    AgentService.Status()
      .then((s) => {
        setStatus(s);
        setBusy(s.status.busy);
      })
      .catch((e) => setError(errMsg(e)));
  }, []);
  const loadTranscript = useCallback(() => {
    AgentService.Transcript()
      .then((t) => setMessages(t ?? []))
      .catch(() => setMessages([]));
  }, []);
  const loadModels = useCallback(() => {
    AgentService.Models()
      .then((m) => setModels(m ?? []))
      .catch(() => setModels([]));
  }, []);

  useEffect(() => {
    loadStatus();
    loadTranscript();
    loadModels();
  }, [loadStatus, loadTranscript, loadModels]);

  useEffect(() => {
    const off = Events.On("agent:event", (ev: { data?: UIEvent }) => {
      const d = ev.data;
      if (!d) return;
      switch (d.type) {
        case "text_delta":
          setMessages((ms) => {
            const id = d.messageId ?? 0;
            if (ms.some((m) => m.id === id)) return ms.map((m) => (m.id === id ? { ...m, text: m.text + (d.delta ?? "") } : m));
            return [...ms, { id, role: "assistant", text: d.delta ?? "", at: new Date().toISOString(), done: false }];
          });
          break;
        case "agent_start":
          setBusy(true);
          setError("");
          loadTranscript();
          break;
        case "message_end":
        case "tool_start":
        case "tool_end":
          loadTranscript();
          if (d.error) setError(d.error);
          break;
        case "agent_end":
          setBusy(false);
          loadTranscript();
          loadStatus();
          break;
        case "status":
          loadStatus();
          if (d.error) setError(d.error);
          break;
        case "exit":
          setBusy(false);
          loadStatus();
          loadTranscript();
          if (d.error) setError(d.error);
          break;
        case "error":
          setBusy(false);
          loadTranscript();
          if (d.error) setError(d.error);
          break;
      }
    });
    return () => {
      off();
    };
  }, [loadStatus, loadTranscript]);

  const send = useCallback(
    (text: string) => {
      const t = text.trim();
      if (!t) return;
      setError("");
      setBusy(true);
      setInput("");
      AgentService.Prompt(t)
        .then(() => {
          loadStatus();
          loadModels();
        })
        .catch((e) => {
          setError(errMsg(e));
          setBusy(false);
        });
    },
    [loadStatus, loadModels],
  );

  useEffect(() => {
    if (prefill && prefill.key !== lastPrefill.current) {
      lastPrefill.current = prefill.key;
      send(prefill.text);
    }
  }, [prefill, send]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [messages, busy]);

  const st = status?.status;
  const running = !!st?.running;
  const disabled = !!status?.disabled;

  const control = (p: Promise<unknown>, after?: () => void) => {
    setError("");
    p.then(() => {
      loadStatus();
      loadTranscript();
      after?.();
    }).catch((e) => setError(errMsg(e)));
  };

  return (
    <div className="flex h-[calc(100vh-7rem)] max-w-4xl flex-col">
      <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
        {disabled ? (
          <Chip tone="amber">disabled in Settings → Agent</Chip>
        ) : running ? (
          <Chip tone="green">{busy ? "working" : "ready"}</Chip>
        ) : (
          <Chip tone="neutral">stopped</Chip>
        )}
        {st?.version && (
          <span className="text-neutral-500" title={st.path}>
            pi {st.version} ({st.source})
          </span>
        )}
        {!disabled && (
          <label className="flex items-center gap-1 text-neutral-600 dark:text-neutral-400">
            model
            <select
              className="rounded border border-neutral-300 bg-white px-1 py-0.5 text-xs dark:border-neutral-700 dark:bg-neutral-900"
              value={st?.model ?? ""}
              onChange={(e) => control(AgentService.SetModel(e.target.value))}
              title="Ollama model the agent uses (models.json is generated from the Ollama server's list)"
            >
              {(models.length ? models : st?.model ? [{ provider: st.provider, id: st.model, name: st.model }] : []).map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id}
                </option>
              ))}
            </select>
          </label>
        )}
        <span className="ml-auto flex gap-1">
          {busy && (
            <Button onClick={() => control(AgentService.Abort())} title="Interrupt the current run">
              Abort
            </Button>
          )}
          <Button onClick={() => control(AgentService.NewSession(), () => setMessages([]))} disabled={disabled} title="Forget the conversation and start fresh">
            New session
          </Button>
          {running ? (
            <Button onClick={() => control(AgentService.Stop())} title="Stop the pi process (the transcript is kept)">
              Stop
            </Button>
          ) : (
            <Button onClick={() => control(AgentService.Start())} disabled={disabled} title="Start the pi sidecar now (it also starts on the first message)">
              Start
            </Button>
          )}
        </span>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {st?.error && !running && !error && <p className="mb-2 text-xs text-amber-700 dark:text-amber-300">{st.error}</p>}

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto rounded-lg border border-neutral-200 bg-white p-3 text-sm dark:border-neutral-800 dark:bg-neutral-900">
        {messages.length === 0 && (
          <div className="text-xs text-neutral-500">
            <p className="mb-2">
              The agent answers questions about your inbox with tools over GitInbox's own data (judgments, scores, summaries, impact analyses) and read-only
              access to GitHub/GitLab. It can mark threads done or snooze them when you ask, and store draft replies — it never posts anything.
            </p>
            <div className="flex flex-wrap gap-1">
              {SUGGESTIONS.map((s) => (
                <button
                  key={s}
                  type="button"
                  onClick={() => send(s)}
                  disabled={disabled}
                  className="rounded-full bg-neutral-100 px-2 py-0.5 text-left text-neutral-700 hover:bg-neutral-200 dark:bg-neutral-800 dark:text-neutral-300 dark:hover:bg-neutral-700"
                >
                  {s}
                </button>
              ))}
            </div>
          </div>
        )}
        {messages.map((m) => (
          <MessageView key={m.id} m={m} />
        ))}
        {busy && <p className="text-xs text-neutral-500">…</p>}
        <div ref={bottomRef} />
      </div>

      <form
        className="mt-2 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          send(input);
        }}
      >
        <textarea
          className="min-h-[2.5rem] flex-1 resize-y rounded border border-neutral-300 bg-white px-2 py-1 text-sm dark:border-neutral-700 dark:bg-neutral-900"
          placeholder={disabled ? "Enable the agent in Settings → Agent" : "Ask about your inbox… (Enter to send, Shift+Enter for a new line)"}
          value={input}
          disabled={disabled}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              send(input);
            }
          }}
        />
        <Button kind="primary" disabled={disabled || !input.trim()} onClick={() => send(input)}>
          {busy ? "Queue" : "Send"}
        </Button>
      </form>
    </div>
  );
}

function MessageView({ m }: { m: Message }) {
  if (m.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] whitespace-pre-wrap rounded-lg bg-blue-50 px-3 py-2 text-neutral-900 dark:bg-blue-950 dark:text-neutral-100">{m.text}</div>
      </div>
    );
  }
  if (m.role === "tool" && m.tool) {
    const t = m.tool;
    return (
      <details className="rounded border border-neutral-200 bg-neutral-50 px-2 py-1 text-xs dark:border-neutral-800 dark:bg-neutral-950">
        <summary className="cursor-pointer">
          <span className={t.isError ? "text-red-600 dark:text-red-400" : "text-neutral-700 dark:text-neutral-300"}>
            {t.done ? (t.isError ? "✗" : "✓") : "…"} {t.name}
          </span>{" "}
          <span className="text-neutral-500">{compactArgs(t.args)}</span>
        </summary>
        {t.args && Object.keys(t.args as object).length > 0 && (
          <pre className="mt-1 max-h-32 overflow-auto rounded bg-white p-1 text-[11px] dark:bg-neutral-900">{JSON.stringify(t.args, null, 1)}</pre>
        )}
        {t.done && <pre className="mt-1 max-h-64 overflow-auto rounded bg-white p-1 text-[11px] dark:bg-neutral-900">{t.result || "(empty)"}</pre>}
      </details>
    );
  }
  if (m.role === "error") {
    return <p className="text-xs text-red-600 dark:text-red-400">{m.text}</p>;
  }
  return <div className="whitespace-pre-wrap leading-relaxed">{linkify(m.text)}</div>;
}

function compactArgs(args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const s = JSON.stringify(args);
  return s.length > 80 ? s.slice(0, 77) + "…" : s;
}

const URL_RE = /(https?:\/\/[^\s)<>\]]+)/g;

// linkify turns bare URLs (and Markdown links) into clickable anchors; the rest stays plain text.
function linkify(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const md = /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g;
  let last = 0;
  let i = 0;
  const pushPlain = (s: string) => {
    const parts = s.split(URL_RE);
    for (const p of parts) {
      if (!p) continue;
      if (/^https?:\/\//.test(p)) {
        out.push(
          <a key={i++} href={p} className="text-blue-700 underline dark:text-blue-400" onClick={(e) => { e.preventDefault(); openURL(p); }}>
            {p}
          </a>,
        );
      } else out.push(<span key={i++}>{p}</span>);
    }
  };
  for (const m of text.matchAll(md)) {
    pushPlain(text.slice(last, m.index));
    const url = m[2];
    out.push(
      <a key={i++} href={url} className="text-blue-700 underline dark:text-blue-400" onClick={(e) => { e.preventDefault(); openURL(url); }}>
        {m[1]}
      </a>,
    );
    last = (m.index ?? 0) + m[0].length;
  }
  pushPlain(text.slice(last));
  return out;
}
