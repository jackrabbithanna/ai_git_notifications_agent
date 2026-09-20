// Small display helpers shared by the views.

export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return "just now";
  const m = s / 60;
  if (m < 60) return `${Math.floor(m)}m ago`;
  const h = m / 60;
  if (h < 48) return `${Math.floor(h)}h ago`;
  const d = h / 24;
  if (d < 30) return `${Math.floor(d)}d ago`;
  return new Date(iso).toLocaleDateString();
}

export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return "never";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

/** Go time.Duration arrives as nanoseconds. */
export function fmtDuration(ns: number): string {
  const ms = ns / 1e6;
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}

export const REASON_LABEL: Record<string, string> = {
  assign: "assigned",
  author: "author",
  comment: "commented",
  ci_activity: "CI",
  invitation: "invited",
  manual: "subscribed",
  mention: "mentioned",
  review_requested: "review requested",
  security_alert: "security",
  state_change: "state change",
  subscribed: "watching",
  team_mention: "team mentioned",
  approval_requested: "approval requested",
};

export function reasonLabel(r: string): string {
  return REASON_LABEL[r] ?? r.replace(/_/g, " ");
}
