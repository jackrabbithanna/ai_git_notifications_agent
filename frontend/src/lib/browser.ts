import { Browser } from "@wailsio/runtime";

/** Open a URL in the system browser (never inside the webview). */
export function openURL(url: string): void {
  if (!url) return;
  Browser.OpenURL(url).catch((e: unknown) => console.error("open url", e));
}
