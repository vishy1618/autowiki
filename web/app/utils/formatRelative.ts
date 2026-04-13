/**
 * Returns a human-readable relative time string for an ISO date string.
 * Examples: "just now", "5 minutes ago", "3 hours ago", "yesterday", "10 Apr"
 */
export function formatRelative(iso: string): string {
  const date = new Date(iso);
  const now = Date.now();
  const diffMs = now - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffSec < 60) return "just now";
  if (diffMin < 60) return diffMin === 1 ? "1 minute ago" : `${diffMin} minutes ago`;
  if (diffHour < 24) return diffHour === 1 ? "1 hour ago" : `${diffHour} hours ago`;
  if (diffDay === 1) return "yesterday";

  return date.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}
