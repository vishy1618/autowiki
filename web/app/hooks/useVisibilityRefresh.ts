import { useEffect } from "react";

interface Options {
  ready: boolean;
  latestActivityRef: React.RefObject<string | null>;
  onRefresh: () => void;
}

export function useVisibilityRefresh({ ready, latestActivityRef, onRefresh }: Options) {
  useEffect(() => {
    if (!ready) return;
    async function onVisibilityChange() {
      if (document.visibilityState !== "visible") return;
      try {
        const r = await fetch("/api/chat-sessions?limit=1");
        if (!r.ok) return;
        const { sessions } = await r.json() as {
          sessions: Array<{ id: string; last_active_at: string }>;
        };
        if (!sessions.length) return;
        const serverActivity = sessions[0].last_active_at;
        if (latestActivityRef.current && serverActivity <= latestActivityRef.current) return;
        onRefresh();
      } catch {
        // silently ignore
      }
    }
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [ready, latestActivityRef, onRefresh]);
}
