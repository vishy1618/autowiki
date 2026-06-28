import { useCallback, useState } from "react";
import type { ChatItem, VaultChange } from "./useChatHistory";

export interface StreamAttachment {
  path?: string;
}

const RETRY_DELAY_MS = 4000;
const MAX_AUTO_RETRIES = 2;

function isAssistantMessage(item: ChatItem): item is Extract<ChatItem, { role: string }> {
  return "role" in item && (item as { role: string }).role === "assistant";
}

interface Options {
  setMessages: React.Dispatch<React.SetStateAction<ChatItem[]>>;
  navigate: (path: string, opts?: { replace?: boolean }) => void;
}

export function useChatStream({ setMessages, navigate }: Options) {
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function apiFetch(url: string, init?: RequestInit) {
    return fetch(url, init).then((r) => {
      if (r.status === 401) navigate("/login", { replace: true });
      return r;
    });
  }

  const doStream = useCallback(async (text: string, attachments: StreamAttachment[], attempt: number) => {
    setError(null);
    setSending(true);

    setMessages((prev) => [
      ...prev,
      { role: "assistant", content: "", streaming: true },
    ]);

    try {
      const body = new URLSearchParams({ message: text });
      for (const att of attachments) {
        if (att.path) body.append("attachment_ids", att.path);
      }
      const resp = await apiFetch("/api/chat", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: body.toString(),
      });

      if (!resp.ok) {
        if (resp.status >= 500 && attempt < MAX_AUTO_RETRIES) {
          setError("Connection hiccup, retrying automatically…");
          setTimeout(() => doStream(text, attachments, attempt + 1), RETRY_DELAY_MS);
          return;
        }
        const bodyText = (await resp.text().catch(() => "")).trim();
        setMessages((prev) => {
          const next = [...prev];
          const last = next[next.length - 1];
          if (last && isAssistantMessage(last) && (last as { streaming?: boolean }).streaming) {
            next[next.length - 1] = { ...last, content: bodyText, streaming: false, createdAt: new Date().toISOString() };
          }
          return next;
        });
        return;
      }
      if (!resp.body) throw new Error(`Server returned ${resp.status}`);

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let lastEventWasStatus = false;

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        let currentEvent = "";
        for (const line of lines) {
          if (line.startsWith("event: ")) {
            currentEvent = line.slice(7).trim();
          } else if (line.startsWith("data: ")) {
            const data = line.slice(6).trim();
            if (currentEvent === "status") {
              try {
                const { message } = JSON.parse(data) as { message: string };
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last && isAssistantMessage(last)) {
                    next[next.length - 1] = { ...last, statusMessage: message };
                  }
                  return next;
                });
                lastEventWasStatus = true;
              } catch { /* ignore malformed */ }
            } else if (currentEvent === "delta") {
              try {
                const { text: deltaText } = JSON.parse(data) as { text: string };
                const prependNewline = lastEventWasStatus;
                lastEventWasStatus = false;
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last && isAssistantMessage(last)) {
                    const content = (last as { content: string }).content;
                    const prefix = prependNewline && content && !/\s$/.test(content) ? "\n\n" : "";
                    next[next.length - 1] = {
                      ...last,
                      statusMessage: undefined,
                      content: content + prefix + deltaText,
                    };
                  }
                  return next;
                });
              } catch { /* ignore malformed */ }
            } else if (currentEvent === "vault") {
              try {
                const { changes } = JSON.parse(data) as { changes: VaultChange[] };
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last && isAssistantMessage(last)) {
                    const existing = (last as { vaultChanges?: VaultChange[] }).vaultChanges ?? [];
                    next[next.length - 1] = { ...last, vaultChanges: [...existing, ...changes] };
                  }
                  return next;
                });
              } catch { /* ignore malformed */ }
            } else if (currentEvent === "error") {
              try {
                const { message } = JSON.parse(data) as { message: string };
                if (attempt < MAX_AUTO_RETRIES) {
                  setError("Connection hiccup, retrying automatically…");
                  setTimeout(() => doStream(text, attachments, attempt + 1), RETRY_DELAY_MS);
                } else {
                  setError(message);
                }
              } catch {
                setError("An error occurred");
              }
            }
            currentEvent = "";
          }
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "An error occurred");
    } finally {
      setMessages((prev) => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last && isAssistantMessage(last) && (last as { streaming?: boolean }).streaming) {
          const content = (last as { content: string }).content;
          const vaultChanges = (last as { vaultChanges?: VaultChange[] }).vaultChanges;
          if (content || vaultChanges?.length) {
            next[next.length - 1] = { ...last, statusMessage: undefined, streaming: false, createdAt: new Date().toISOString() };
          } else {
            next.pop();
          }
        }
        return next;
      });
      setSending(false);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [setMessages, navigate]);

  return { sending, error, doStream };
}
