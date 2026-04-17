import { memo, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { formatRelative } from "../utils/formatRelative";
import type { Route } from "./+types/home";

export function meta({}: Route.MetaArgs) {
  return [{ title: "autowiki" }];
}

interface VaultChange {
  path: string;
}

interface PendingAttachment {
  localId: string;       // client-side id for tracking
  file: File;
  uploading: boolean;
  path?: string;         // vault-relative path returned by server
  previewUrl?: string;   // object URL for images
}

interface Message {
  kind?: "message";
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
  statusMessage?: string;
  vaultChanges?: VaultChange[];
  attachments?: PendingAttachment[];
  createdAt?: string; // ISO string; absent while streaming
}

interface SessionDivider {
  kind: "divider";
  id: string;
}

type ChatItem = Message | SessionDivider;

function isMessage(item: ChatItem): item is Message {
  return "role" in item;
}

export default function Home() {
  const navigate = useNavigate();
  const [ready, setReady] = useState(false);
  const [messages, setMessages] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pendingAttachments, setPendingAttachments] = useState<PendingAttachment[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const [historyOffset, setHistoryOffset] = useState(0);
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    fetch("/api/auth/me")
      .then((r) => {
        if (r.status === 401) {
          navigate("/login", { replace: true });
          return null;
        }
        return r.json();
      })
      .then((data) => {
        if (data?.email) setReady(true);
      })
      .catch(() => navigate("/login", { replace: true }));
  }, [navigate]);

  useEffect(() => {
    if (ready) inputRef.current?.focus();
  }, [ready]);

  const HISTORY_LIMIT = 3;

  async function loadHistory(offset: number, prepend: boolean) {
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
      // API returns newest-first; reverse so we display oldest-first.
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
      // silently ignore history load failures
    } finally {
      setHistoryLoading(false);
    }
  }

  useEffect(() => {
    if (!ready) return;
    loadHistory(0, false);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    if (!hasMoreHistory || historyLoading) return;
    const sentinel = sentinelRef.current;
    if (!sentinel) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) {
        loadHistory(historyOffset, true);
      }
    });
    observer.observe(sentinel);
    return () => observer.disconnect();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasMoreHistory, historyLoading, historyOffset]);

  // Wrapper around fetch that redirects to /login on any 401 response.
  function apiFetch(url: string, init?: RequestInit) {
    return fetch(url, init).then((r) => {
      if (r.status === 401) navigate("/login", { replace: true });
      return r;
    });
  }

  async function uploadFile(file: File) {
    const localId = crypto.randomUUID();
    const previewUrl = file.type.startsWith("image/")
      ? URL.createObjectURL(file)
      : undefined;

    setPendingAttachments((prev) => [
      ...prev,
      { localId, file, uploading: true, previewUrl },
    ]);

    try {
      const form = new FormData();
      form.append("file", file);
      const resp = await apiFetch("/api/attachments", { method: "POST", body: form });
      if (!resp.ok) throw new Error(`Upload failed: ${resp.status}`);
      const data = await resp.json() as { path: string };
      setPendingAttachments((prev) =>
        prev.map((a) =>
          a.localId === localId ? { ...a, uploading: false, path: data.path } : a
        )
      );
    } catch {
      // Remove the failed attachment.
      setPendingAttachments((prev) => prev.filter((a) => a.localId !== localId));
    }
  }

  function handleFileInputChange(e: React.ChangeEvent<HTMLInputElement>) {
    for (const file of Array.from(e.target.files ?? [])) {
      uploadFile(file);
    }
    // Reset so the same file can be re-selected.
    e.target.value = "";
  }

  function handleDragOver(e: React.DragEvent) {
    e.preventDefault();
    setDragOver(true);
  }

  function handleDragLeave() {
    setDragOver(false);
  }

  function handleDrop(e: React.DragEvent) {
    e.preventDefault();
    setDragOver(false);
    for (const file of Array.from(e.dataTransfer.files)) {
      uploadFile(file);
    }
  }

  function dismissAttachment(localId: string) {
    setPendingAttachments((prev) => {
      const att = prev.find((a) => a.localId === localId);
      if (att?.previewUrl) URL.revokeObjectURL(att.previewUrl);
      return prev.filter((a) => a.localId !== localId);
    });
  }

  async function sendMessage() {
    const text = input.trim();
    if (!text || sending || pendingAttachments.some((a) => a.uploading)) return;

    const readyAttachments = pendingAttachments.filter((a) => !a.uploading && a.path);

    setInput("");
    setPendingAttachments([]);
    setError(null);
    setSending(true);
    inputRef.current?.focus();

    const sentAt = new Date().toISOString();
    setMessages((prev) => [
      ...prev,
      { role: "user", content: text, attachments: readyAttachments, createdAt: sentAt },
      { role: "assistant", content: "", streaming: true },
    ]);

    try {
      const body = new URLSearchParams({ message: text });
      for (const att of readyAttachments) {
        body.append("attachment_ids", att.path!);
      }
      const resp = await apiFetch("/api/chat", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: body.toString(),
      });

      if (!resp.ok) {
        const bodyText = (await resp.text().catch(() => "")).trim();
        setMessages((prev) => {
          const next = [...prev];
          const last = next[next.length - 1];
          if (last && isMessage(last) && last.role === "assistant" && last.streaming) {
            next[next.length - 1] = { ...last, content: bodyText, streaming: false, createdAt: new Date().toISOString() };
          }
          return next;
        });
        return;
      }
      if (!resp.body) {
        throw new Error(`Server returned ${resp.status}`);
      }

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
                    if (last && isMessage(last) && last.role === "assistant") {
                      next[next.length - 1] = { ...last, statusMessage: message };
                    }
                    return next;
                  });
                  lastEventWasStatus = true;
                } catch {
                  // ignore malformed status
                }
              } else if (currentEvent === "delta") {
              try {
                const { text } = JSON.parse(data) as { text: string };
                const prependNewline = lastEventWasStatus;
                lastEventWasStatus = false;
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last && isMessage(last) && last.role === "assistant") {
                    const prefix =
                      prependNewline && last.content && !/\s$/.test(last.content)
                        ? "\n\n"
                        : "";
                    next[next.length - 1] = {
                      ...last,
                      statusMessage: undefined,
                      content: last.content + prefix + text,
                    };
                  }
                  return next;
                });
              } catch {
                // ignore malformed delta
              }
            } else if (currentEvent === "vault") {
              try {
                const { changes } = JSON.parse(data) as { changes: VaultChange[] };
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last && isMessage(last) && last.role === "assistant") {
                    next[next.length - 1] = { ...last, vaultChanges: changes };
                  }
                  return next;
                });
              } catch {
                // ignore malformed vault event
              }
            } else if (currentEvent === "error") {
              try {
                const { message } = JSON.parse(data) as { message: string };
                setError(message);
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
      // Finalise or discard the streaming bubble.
      setMessages((prev) => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last && isMessage(last) && last.role === "assistant" && last.streaming) {
          if (last.content) {
            next[next.length - 1] = { ...last, statusMessage: undefined, streaming: false, createdAt: new Date().toISOString() };
          } else {
            next.pop();
          }
        }
        return next;
      });
      setSending(false);
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  }

  async function handleSignOut() {
    await fetch("/api/auth/logout", { method: "POST" });
    navigate("/login", { replace: true });
  }

  if (!ready) return null;

  return (
    <div
      style={styles.layout}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {/* Drag-and-drop overlay */}
      {dragOver && <div style={styles.dropOverlay}>Drop files to attach</div>}

      {/* Hidden file input */}
      <input
        ref={fileInputRef}
        data-testid="file-input"
        type="file"
        multiple
        style={{ display: "none" }}
        onChange={handleFileInputChange}
      />

      {/* Header */}
      <header style={styles.header}>
        <span style={styles.logo}>auto<span style={styles.logoWiki}>wiki</span></span>
        <button onClick={handleSignOut} style={styles.signOutBtn}>
          Sign out
        </button>
      </header>

      {/* Message thread */}
      <MessageThread
        messages={messages}
        error={error}
        historyLoading={historyLoading}
        hasMoreHistory={hasMoreHistory}
        bottomRef={bottomRef}
        sentinelRef={sentinelRef}
      />

      {/* Input bar */}
      <div style={styles.inputBar}>
        {/* Pending attachment chips */}
        {pendingAttachments.length > 0 && (
          <div style={styles.attachmentChips}>
            {pendingAttachments.map((att) => (
              <div key={att.localId} style={styles.chip}>
                {att.previewUrl ? (
                  <img src={att.previewUrl} alt={att.file.name} style={styles.chipThumb} />
                ) : (
                  <span style={styles.chipFileExt}>
                    {att.file.name.split(".").pop()?.toUpperCase() ?? "FILE"}
                  </span>
                )}
                <span style={styles.chipName}>{att.file.name}</span>
                {att.uploading && <span style={styles.chipStatus}>↑</span>}
                {!att.uploading && (
                  <button
                    onClick={() => dismissAttachment(att.localId)}
                    style={styles.chipDismiss}
                    aria-label="Remove attachment"
                  >
                    ×
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
        <div className="input-box" style={styles.inputBox}>
          <textarea
            ref={inputRef}
            style={styles.textarea}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Message autowiki…"
            rows={2}
          />
          <div style={styles.inputBoxFooter}>
            <button
              onClick={() => fileInputRef.current?.click()}
              style={styles.attachBtn}
              title="Attach file"
              aria-label="Attach file"
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/>
              </svg>
            </button>
            <button
              onClick={sendMessage}
              disabled={sending || !input.trim() || pendingAttachments.some((a) => a.uploading)}
              style={{
                ...styles.sendIconBtn,
                opacity: input.trim() && !sending ? 1 : 0,
                pointerEvents: input.trim() && !sending ? "auto" : "none",
              }}
              aria-label="Send message"
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
                <path d="M12 4l8 16H4z"/>
              </svg>
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

interface MessageThreadProps {
  messages: ChatItem[];
  error: string | null;
  historyLoading: boolean;
  hasMoreHistory: boolean;
  bottomRef: React.RefObject<HTMLDivElement | null>;
  sentinelRef: React.RefObject<HTMLDivElement | null>;
}

const MessageThread = memo(function MessageThread({
  messages,
  error,
  historyLoading,
  hasMoreHistory,
  bottomRef,
  sentinelRef,
}: MessageThreadProps) {
  return (
    <div style={styles.thread}>
      {hasMoreHistory && <div ref={sentinelRef} />}
      {historyLoading && <p style={styles.historyLoading}>Loading…</p>}
      {messages.length === 0 && !historyLoading && (
        <p style={styles.emptyHint}>Start a conversation…</p>
      )}
      {messages.map((item, i) => {
        if ("kind" in item && item.kind === "divider") {
          return <hr key={item.id} data-testid="session-divider" style={styles.sessionDivider} />;
        }
        const msg = item as Message;
        return (
          <div
            key={i}
            style={{
              ...styles.bubble,
              ...(msg.role === "user" ? styles.userBubble : styles.assistantBubble),
            }}
          >
            <span style={styles.roleLabel}>
              {msg.role === "user" ? "You" : "Assistant"}
            </span>
            {msg.createdAt && (
              <span
                style={styles.timestamp}
                title={new Date(msg.createdAt).toLocaleString()}
              >
                {formatRelative(msg.createdAt)}
              </span>
            )}
            {msg.role === "user" && msg.attachments && msg.attachments.length > 0 && (
              <div style={styles.attachmentRow}>
                {msg.attachments.map((att) =>
                  att.previewUrl ? (
                    <img
                      key={att.localId}
                      src={att.previewUrl}
                      alt={att.file.name}
                      style={styles.attachmentThumb}
                    />
                  ) : (
                    <div key={att.localId} style={styles.attachmentFileCard}>
                      <span style={styles.attachmentFileExt}>
                        {att.file.name.split(".").pop()?.toUpperCase() ?? "FILE"}
                      </span>
                      <span style={styles.attachmentFileName}>{att.file.name}</span>
                    </div>
                  )
                )}
              </div>
            )}
            {msg.role === "assistant" ? (
              <div style={styles.bubbleText}>
                {msg.statusMessage && (
                  <em style={styles.statusLine}>{msg.statusMessage}</em>
                )}
                <div className="md"><Markdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{msg.content}</Markdown></div>
                {msg.streaming && <span style={styles.cursor}>▌</span>}
                {!msg.streaming && msg.vaultChanges && msg.vaultChanges.length > 0 && (
                  <details style={styles.vaultSummary}>
                    <summary style={styles.vaultSummaryTitle}>Saved to vault</summary>
                    <ul style={styles.vaultList}>
                      {msg.vaultChanges.map((c) => (
                        <li key={c.path} style={styles.vaultListItem}>{c.path}</li>
                      ))}
                    </ul>
                  </details>
                )}
              </div>
            ) : (
              <p style={{ ...styles.bubbleText, whiteSpace: "pre-wrap" }}>{msg.content}</p>
            )}
          </div>
        );
      })}
      {error && <p style={styles.errorText}>Error: {error}</p>}
      <div ref={bottomRef} />
    </div>
  );
});

const markdownComponents = {
  // External links open in new tab.
  a: (props: React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    // eslint-disable-next-line jsx-a11y/anchor-has-content
    <a target="_blank" rel="noreferrer" {...props} />
  ),
  // Tables need explicit styles because Tailwind preflight collapses borders.
  table: (props: React.HTMLAttributes<HTMLTableElement>) => (
    <table style={styles.mdTable} {...props} />
  ),
  th: (props: React.ThHTMLAttributes<HTMLTableCellElement>) => (
    <th style={styles.mdTh} {...props} />
  ),
  td: (props: React.TdHTMLAttributes<HTMLTableCellElement>) => (
    <td style={styles.mdTd} {...props} />
  ),
};

const styles: Record<string, React.CSSProperties> = {
  layout: {
    display: "flex",
    flexDirection: "column",
    height: "100vh",
    fontFamily: "Inter, system-ui, sans-serif",
    background: "#f9f9f9",
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0.75rem 1.5rem",
    borderBottom: "1px solid #e5e5e5",
    background: "#fff",
  },
  logo: {
    fontWeight: 600,
    fontSize: "1rem",
    color: "#111",
  },
  logoWiki: {
    color: "#1d4ed8",
  },
  signOutBtn: {
    background: "none",
    border: "1px solid #ccc",
    borderRadius: "6px",
    padding: "0.3rem 0.75rem",
    cursor: "pointer",
    fontSize: "0.85rem",
    color: "#555",
  },
  thread: {
    flex: 1,
    overflowY: "auto",
    padding: "1.5rem",
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
  },
  emptyHint: {
    color: "#aaa",
    textAlign: "center",
    marginTop: "4rem",
    fontSize: "0.95rem",
  },
  bubble: {
    maxWidth: "75%",
    padding: "0.75rem 1rem",
    borderRadius: "12px",
    lineHeight: 1.6,
  },
  userBubble: {
    alignSelf: "flex-end",
    background: "#111",
    color: "#fff",
  },
  assistantBubble: {
    alignSelf: "flex-start",
    background: "#fff",
    border: "1px solid #e5e5e5",
    color: "#111",
  },
  roleLabel: {
    fontSize: "0.7rem",
    fontWeight: 600,
    textTransform: "uppercase",
    letterSpacing: "0.05em",
    opacity: 0.5,
    display: "block",
    marginBottom: "0.25rem",
  },
  bubbleText: {
    margin: 0,
  },
  cursor: {
    animation: "blink 1s step-end infinite",
    marginLeft: "1px",
  },
  errorText: {
    color: "#c0392b",
    fontSize: "0.875rem",
    textAlign: "center",
  },
  inputBar: {
    display: "flex",
    flexDirection: "column" as const,
    gap: "0.5rem",
    padding: "0.75rem 1.5rem 1.25rem",
    borderTop: "1px solid #e5e5e5",
    background: "#fff",
  },
  inputBox: {
    display: "flex",
    flexDirection: "column" as const,
    borderRadius: "14px",
    background: "#fff",
  },
  textarea: {
    resize: "none",
    border: "none",
    outline: "none",
    padding: "0.75rem 1rem 0.25rem",
    fontSize: "0.95rem",
    fontFamily: "inherit",
    lineHeight: 1.5,
    color: "#111",
    background: "transparent",
  },
  inputBoxFooter: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    padding: "0.25rem 0.5rem 0.5rem",
  },
  attachBtn: {
    background: "none",
    border: "none",
    cursor: "pointer",
    padding: "0.35rem",
    borderRadius: "6px",
    color: "#888",
    display: "flex",
    alignItems: "center",
  },
  sendIconBtn: {
    width: "30px",
    height: "30px",
    borderRadius: "8px",
    background: "#111",
    color: "#fff",
    border: "none",
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    transition: "opacity 0.15s",
  },
  dropOverlay: {
    position: "fixed" as const,
    inset: 0,
    background: "rgba(0,0,0,0.35)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    fontSize: "1.25rem",
    color: "#fff",
    fontWeight: 600,
    zIndex: 100,
    pointerEvents: "none" as const,
  },
  attachmentRow: {
    display: "flex",
    flexWrap: "wrap" as const,
    gap: "0.5rem",
    marginBottom: "0.4rem",
  },
  attachmentThumb: {
    width: "80px",
    height: "80px",
    objectFit: "cover" as const,
    borderRadius: "6px",
    border: "1px solid #e5e5e5",
  },
  attachmentFileCard: {
    width: "80px",
    height: "80px",
    borderRadius: "6px",
    border: "1px solid #e5e5e5",
    display: "flex",
    flexDirection: "column" as const,
    alignItems: "center",
    justifyContent: "center",
    gap: "0.25rem",
    background: "#f5f5f5",
    padding: "0.4rem",
    overflow: "hidden",
  },
  attachmentFileExt: {
    fontSize: "0.7rem",
    fontWeight: 700,
    color: "#555",
    letterSpacing: "0.05em",
  },
  attachmentFileName: {
    fontSize: "0.65rem",
    color: "#888",
    textAlign: "center" as const,
    wordBreak: "break-all" as const,
  },
  attachmentChips: {
    display: "flex",
    flexWrap: "wrap" as const,
    gap: "0.4rem",
  },
  chip: {
    display: "flex",
    alignItems: "center",
    gap: "0.35rem",
    background: "#f0f0f0",
    borderRadius: "20px",
    padding: "0.2rem 0.5rem 0.2rem 0.3rem",
    fontSize: "0.8rem",
    color: "#333",
    maxWidth: "200px",
  },
  chipThumb: {
    width: "22px",
    height: "22px",
    objectFit: "cover" as const,
    borderRadius: "50%",
  },
  chipFileExt: {
    fontSize: "0.65rem",
    fontWeight: 700,
    background: "#ccc",
    borderRadius: "4px",
    padding: "1px 4px",
    color: "#444",
  },
  chipName: {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    flex: 1,
  },
  chipStatus: {
    fontSize: "0.75rem",
    color: "#888",
  },
  chipDismiss: {
    background: "none",
    border: "none",
    cursor: "pointer",
    fontSize: "0.9rem",
    color: "#888",
    padding: 0,
    lineHeight: 1,
  },
  timestamp: {
    display: "block",
    fontSize: "0.7rem",
    color: "#aaa",
    marginBottom: "0.35rem",
  },
  statusLine: {
    display: "block",
    color: "#888",
    fontSize: "0.85rem",
    marginBottom: "0.4rem",
  },
  vaultSummary: {
    marginTop: "0.75rem",
    borderTop: "1px solid #e5e5e5",
    paddingTop: "0.5rem",
  },
  vaultSummaryTitle: {
    fontSize: "0.75rem",
    fontWeight: 600,
    color: "#555",
    cursor: "pointer",
    userSelect: "none" as const,
  },
  vaultList: {
    margin: "0.25rem 0 0 1rem",
    padding: 0,
    listStyle: "disc",
  },
  vaultListItem: {
    fontSize: "0.8rem",
    color: "#555",
    fontFamily: "monospace",
    marginTop: "0.2rem",
  },
  mdTable: {
    borderCollapse: "collapse",
    width: "100%",
    marginBottom: "1rem",
    whiteSpace: "normal",
  },
  mdTh: {
    border: "1px solid #e5e5e5",
    padding: "0.4rem 0.75rem",
    background: "#f5f5f5",
    fontWeight: 600,
    textAlign: "left",
    whiteSpace: "normal",
  },
  mdTd: {
    border: "1px solid #e5e5e5",
    padding: "0.4rem 0.75rem",
    whiteSpace: "normal",
  },
  sessionDivider: {
    border: "none",
    borderTop: "1px solid #e5e5e5",
    margin: "0.5rem 0",
    opacity: 0.6,
  },
  historyLoading: {
    color: "#aaa",
    textAlign: "center",
    fontSize: "0.85rem",
    margin: "0.5rem 0",
  },
};
