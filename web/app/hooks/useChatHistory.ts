import { useCallback, useEffect, useRef, useState } from "react";

export interface VaultChange {
  path: string;
}

export interface Message {
  kind?: "message";
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
  statusMessage?: string;
  vaultChanges?: VaultChange[];
  attachments?: unknown[];
  createdAt?: string;
}

export interface SessionDivider {
  kind: "divider";
  id: string;
}

export type ChatItem = Message | SessionDivider;

const HISTORY_LIMIT = 3;

export function useChatHistory({ ready }: { ready: boolean }) {
  const [messages, setMessages] = useState<ChatItem[]>([]);
  const [historyOffset, setHistoryOffset] = useState(0);
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const latestActivityRef = useRef<string | null>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  const loadHistory = useCallback(async (offset: number, prepend: boolean) => {
    setHistoryLoading(true);
    try {
      const r = await fetch(`/api/chat-sessions?limit=${HISTORY_LIMIT}&offset=${offset}`);
      if (!r.ok) return;
      const { sessions } = await r.json() as {
        sessions: Array<{ id: string; created_at: string; last_active_at: string }>;
      };
      const more = sessions.length === HISTORY_LIMIT;
      setHasMoreHistory(more);
      setHistoryOffset(offset + sessions.length);
      if (!sessions.length) return;
      if (!prepend) latestActivityRef.current = sessions[0].last_active_at;
      const oldest = [...sessions].reverse();
      const items: ChatItem[] = [];
      for (let i = 0; i < oldest.length; i++) {
        const session = oldest[i];
        const mr = await fetch(`/api/chat-sessions/${session.id}`);
        if (!mr.ok) continue;
        const { messages: msgs } = await mr.json() as {
          messages: Array<{ id: string; role: "user" | "assistant"; content: string; created_at: string; vault_changes?: string[] }>;
        };
        if (i > 0 || prepend) {
          items.push({ kind: "divider", id: `divider-${session.id}` });
        }
        for (const msg of msgs) {
          items.push({
            role: msg.role,
            content: msg.content,
            createdAt: msg.created_at,
            vaultChanges: msg.vault_changes?.map((path) => ({ path })),
          });
        }
      }
      if (prepend) {
        setMessages((prev) => [...items, ...prev]);
      } else {
        setMessages(items);
      }
    } catch {
      // silently ignore
    } finally {
      setHistoryLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!ready) return;
    loadHistory(0, false);
  }, [ready, loadHistory]);

  return { messages, setMessages, historyOffset, hasMoreHistory, historyLoading, loadHistory, latestActivityRef, sentinelRef };
}
