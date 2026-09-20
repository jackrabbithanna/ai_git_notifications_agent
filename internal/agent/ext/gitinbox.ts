// GitInbox extension for pi (pi.dev): tools over the app's loopback API.
// Written by GitInbox into its agent directory on every start; do not edit.
// The API URL and bearer token come from the environment the app sets
// (GITINBOX_API_URL / GITINBOX_API_TOKEN). Every tool is read-only against
// the forges; the "write" tools change local triage state or store a draft.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const API = (process.env.GITINBOX_API_URL ?? "").replace(/\/$/, "");
const TOKEN = process.env.GITINBOX_API_TOKEN ?? "";
const MAX_OUTPUT = 40_000; // bytes handed back to the model per tool call

type Json = Record<string, unknown>;

async function api(path: string, init?: { method?: string; body?: unknown; signal?: AbortSignal }): Promise<unknown> {
  if (!API) throw new Error("GITINBOX_API_URL is not set; start the agent from GitInbox");
  const res = await fetch(API + path, {
    method: init?.method ?? "GET",
    headers: { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json" },
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
    signal: init?.signal,
  });
  const text = await res.text();
  let data: unknown = text;
  try {
    data = JSON.parse(text);
  } catch {
    /* plain text */
  }
  if (!res.ok) {
    const msg = typeof data === "object" && data && "error" in data ? String((data as Json).error) : text;
    throw new Error(`GitInbox API ${res.status}: ${msg}`);
  }
  return data;
}

function out(data: unknown) {
  let text = typeof data === "string" ? data : JSON.stringify(data, null, 1);
  if (text.length > MAX_OUTPUT) {
    text = text.slice(0, MAX_OUTPUT) + `\n\n[truncated: ${text.length} bytes; ask for fewer items or a narrower query]`;
  }
  return { content: [{ type: "text" as const, text }], details: {} };
}

function q(params: Record<string, string | number | boolean | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "" && v !== null) sp.set(k, String(v));
  const s = sp.toString();
  return s ? `?${s}` : "";
}

const account = Type.String({ description: "Account as login@host (see list_accounts) or its numeric id" });
const accountOpt = Type.Optional(Type.String({ description: "Account as login@host or id; omit for all accounts" }));
const threadId = Type.String({ description: "Thread id exactly as returned by other tools (e.g. 25702457595, todo:123, gl:70:Issue:456)" });
const repo = Type.String({ description: "Repository path, e.g. civicrm/civicrm-core or dev/core" });
const number = Type.Number({ description: "Issue / pull request / merge request number" });

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "list_accounts",
    label: "List accounts",
    description: "List the configured GitHub/GitLab accounts (ref, forge, host, write mode). Call this first if you do not know the account refs.",
    parameters: Type.Object({}),
    async execute(_id, _params, signal) {
      return out(await api("/v1/accounts", { signal }));
    },
  });

  pi.registerTool({
    name: "list_top_priority",
    label: "Top priority threads",
    description:
      "The highest-priority unread notification threads (pinned first, then priority percent). bucket=needs_me returns only threads judged to need the user; bucket=all returns everything unread. Each row has account, thread_id, repo, number, title, activity kind, the user's relations, judged category, next action, bucket, impact level and a one-line summary when one exists.",
    parameters: Type.Object({
      limit: Type.Optional(Type.Number({ description: "Rows to return (default 15, max 100)" })),
      bucket: Type.Optional(Type.String({ description: "needs_me | normal | resolved | all (default all)" })),
      account: accountOpt,
    }),
    async execute(_id, p, signal) {
      return out(await api("/v1/threads" + q({ limit: Math.min(p.limit ?? 15, 100), bucket: p.bucket ?? "all", account: p.account }), { signal }));
    },
  });

  pi.registerTool({
    name: "search_threads",
    label: "Search threads",
    description:
      "Search notification threads by words in the repository, title, number, activity kind, relation or actor (all words must match, case-insensitive), newest activity first. Set include_read to also search read, done and snoozed threads.",
    parameters: Type.Object({
      query: Type.String({ description: "Words to match, e.g. 'civicrm-core afform' or '#36990'" }),
      limit: Type.Optional(Type.Number({ description: "Rows to return (default 20, max 100)" })),
      include_read: Type.Optional(Type.Boolean({ description: "Include read/done/snoozed threads (default false)" })),
      account: accountOpt,
    }),
    async execute(_id, p, signal) {
      return out(await api("/v1/threads" + q({ q: p.query, limit: Math.min(p.limit ?? 20, 100), include_read: p.include_read ?? false, account: p.account }), { signal }));
    },
  });

  pi.registerTool({
    name: "get_thread",
    label: "Get thread",
    description:
      "Full view of one notification thread: the item (body, state, labels, author), the latest activity (author, text), the user's relation, the judged answers with probabilities, the priority score, the stored summary, the impact analysis for a pull/merge request, and any saved draft.",
    parameters: Type.Object({ account, thread_id: threadId }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/threads/${encodeURIComponent(p.account)}/${encodeURIComponent(p.thread_id)}`, { signal }));
    },
  });

  pi.registerTool({
    name: "find_thread",
    label: "Find thread by item",
    description: "Find the notification thread for a repository item (issue, pull request or merge request number) on an account and return its full view (same as get_thread).",
    parameters: Type.Object({ account, repo, number }),
    async execute(_id, p, signal) {
      return out(await api("/v1/threads/find" + q({ account: p.account, repo: p.repo, number: p.number }), { signal }));
    },
  });

  pi.registerTool({
    name: "get_thread_summary",
    label: "Thread summary",
    description: "The stored Ollama summary of a thread (summary, key points, asks of the user, what changed since last read). Generates one if missing; regenerate=true forces a fresh one (takes seconds).",
    parameters: Type.Object({ account, thread_id: threadId, regenerate: Type.Optional(Type.Boolean()) }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/threads/${encodeURIComponent(p.account)}/${encodeURIComponent(p.thread_id)}/summary` + q({ generate: p.regenerate ?? false, force: p.regenerate ?? false }), { signal }));
    },
  });

  pi.registerTool({
    name: "get_thread_comments",
    label: "Thread discussion",
    description: "The item's description and its discussion fetched live from the forge (issue comments; for pull requests also reviews and review comments; GitLab notes), oldest first, most recent `limit` entries. Read-only.",
    parameters: Type.Object({ account, thread_id: threadId, limit: Type.Optional(Type.Number({ description: "Most recent entries to return (default 30, max 100)" })) }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/threads/${encodeURIComponent(p.account)}/${encodeURIComponent(p.thread_id)}/comments` + q({ limit: Math.min(p.limit ?? 30, 100) }), { signal }));
    },
  });

  pi.registerTool({
    name: "list_mine",
    label: "My items",
    description: "Issues and pull/merge requests where the user is assigned, mentioned, review-requested or the author (from forge searches, so complete even without notifications). Open items by default.",
    parameters: Type.Object({ account: accountOpt, include_closed: Type.Optional(Type.Boolean()) }),
    async execute(_id, p, signal) {
      return out(await api("/v1/mine" + q({ account: p.account, closed: p.include_closed ?? false }), { signal }));
    },
  });

  pi.registerTool({
    name: "list_impact",
    label: "Impact analyses",
    description: "Analysed pull/merge requests in the user's impact-profile repositories with their downstream impact level (none/possible/likely/certain), change kind, layers touched and state. landed=true lists merged ones, landed=false open ones; omit for both.",
    parameters: Type.Object({
      account: accountOpt,
      min_level: Type.Optional(Type.Number({ description: "0 none, 1 possible, 2 likely, 3 certain (default 1)" })),
      landed: Type.Optional(Type.Boolean()),
      limit: Type.Optional(Type.Number({ description: "default 30" })),
    }),
    async execute(_id, p, signal) {
      return out(await api("/v1/impact" + q({ account: p.account, min: p.min_level ?? 1, landed: p.landed, limit: p.limit ?? 30 }), { signal }));
    },
  });

  pi.registerTool({
    name: "get_pr_analysis",
    label: "PR impact analysis",
    description:
      "The impact analysis of one pull/merge request: layers touched, signal keywords, public-surface hits, the judged change kind and downstream impact with probabilities, and the written impact note when one exists. Runs the analysis if none is stored (analyze=true forces a re-run; note=true writes the note with the Ollama model, which can take a while).",
    parameters: Type.Object({ account, repo, number, analyze: Type.Optional(Type.Boolean()), note: Type.Optional(Type.Boolean()) }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/impact/${encodeURIComponent(p.account)}` + q({ repo: p.repo, number: p.number, analyze: p.analyze ?? false, note: p.note ?? false }), { signal }));
    },
  });

  pi.registerTool({
    name: "get_pr_changes",
    label: "PR changed files",
    description: "Metadata and changed files of a pull/merge request fetched live from the forge (path, status, additions, deletions; include_patches=true adds trimmed diffs). Use for code-level questions; output is capped, so narrow with max_files.",
    parameters: Type.Object({
      account,
      repo,
      number,
      max_files: Type.Optional(Type.Number({ description: "default 40" })),
      include_patches: Type.Optional(Type.Boolean({ description: "include diff hunks (default false)" })),
    }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/changes/${encodeURIComponent(p.account)}` + q({ repo: p.repo, number: p.number, max_files: p.max_files ?? 40, patches: p.include_patches ?? false }), { signal }));
    },
  });

  const action = (name: string, label: string, description: string, act: string, hours = false) =>
    pi.registerTool({
      name,
      label,
      description,
      parameters: hours
        ? Type.Object({ account, thread_id: threadId, hours: Type.Optional(Type.Number({ description: "Hours to hide the thread (default 24)" })) })
        : Type.Object({ account, thread_id: threadId }),
      async execute(_id, p: { account: string; thread_id: string; hours?: number }, signal) {
        return out(await api(`/v1/threads/${encodeURIComponent(p.account)}/${encodeURIComponent(p.thread_id)}/${act}`, { method: "POST", body: hours ? { hours: p.hours ?? 24 } : {}, signal }));
      },
    });
  action("mark_done", "Mark done", "Mark a thread done in GitInbox (hidden until new activity). Mirrored to the forge only when the account's write mode is 'notifications'. Only do this when the user asks.", "done");
  action("mark_read", "Mark read", "Mark a thread read locally (GitHub also when write mode allows; never on GitLab). Only do this when the user asks.", "read");
  action("undo_done", "Undo done", "Bring a thread marked done back into the inbox.", "undone");
  action("snooze", "Snooze", "Hide a thread for a number of hours (default 24); new activity un-snoozes it. Only do this when the user asks.", "snooze", true);

  pi.registerTool({
    name: "draft_reply",
    label: "Draft reply",
    description: "Store a draft reply for a thread in GitInbox so the user can copy it. Nothing is ever posted to GitHub or GitLab. Write the draft in the user's voice, addressing the latest ask.",
    parameters: Type.Object({ account, thread_id: threadId, text: Type.String({ description: "The draft text (Markdown)" }) }),
    async execute(_id, p, signal) {
      return out(await api("/v1/drafts", { method: "POST", body: { account: p.account, thread_id: p.thread_id, text: p.text, model: process.env.PI_MODEL ?? "" }, signal }));
    },
  });

  pi.registerTool({
    name: "github_read",
    label: "GitHub read tool",
    description:
      "Call one read-only GitHub MCP tool for a GitHub account: get_me, list_notifications, get_notification_details (notificationID), issue_read (owner, repo, issue_number, method: get|get_comments|get_sub_issues|get_labels), pull_request_read (owner, repo, pullNumber, method: get|get_diff|get_status|get_files|get_commits|get_review_comments|get_reviews|get_comments|get_check_runs), search_issues / search_pull_requests (query, perPage), list_pull_requests (owner, repo, state). Anything else is refused. Returns the tool's JSON.",
    parameters: Type.Object({
      account,
      tool: Type.String({ description: "Tool name" }),
      args: Type.Optional(Type.Object({}, { additionalProperties: true, description: "Tool arguments as an object" })),
    }),
    async execute(_id, p, signal) {
      return out(await api(`/v1/github/${encodeURIComponent(p.account)}/call`, { method: "POST", body: { tool: p.tool, args: p.args ?? {} }, signal }));
    },
  });
}
