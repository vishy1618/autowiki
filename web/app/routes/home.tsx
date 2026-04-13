import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
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
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
  statusMessage?: string;
  vaultChanges?: VaultChange[];
  attachments?: PendingAttachment[];
}

export default function Home() {
  const navigate = useNavigate();
  const [ready, setReady] = useState(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pendingAttachments, setPendingAttachments] = useState<PendingAttachment[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

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

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

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

    setMessages((prev) => [
      ...prev,
      { role: "user", content: text, attachments: readyAttachments },
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

      if (!resp.ok || !resp.body) {
        throw new Error(`Server returned ${resp.status}`);
      }

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

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
                    if (last?.role === "assistant") {
                      next[next.length - 1] = { ...last, statusMessage: message };
                    }
                    return next;
                  });
                } catch {
                  // ignore malformed status
                }
              } else if (currentEvent === "delta") {
              try {
                const { text } = JSON.parse(data) as { text: string };
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last?.role === "assistant") {
                    next[next.length - 1] = {
                      ...last,
                      statusMessage: undefined,
                      content: last.content + text,
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
                  if (last?.role === "assistant") {
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
      // Finalise the streaming bubble.
      setMessages((prev) => {
        const next = [...prev];
        const last = next[next.length - 1];
        if (last?.role === "assistant" && last.streaming) {
          next[next.length - 1] = { ...last, streaming: false };
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
        <span style={styles.logo}>autowiki</span>
        <button onClick={handleSignOut} style={styles.signOutBtn}>
          Sign out
        </button>
      </header>

      {/* Message thread */}
      <div style={styles.thread}>
        {messages.length === 0 && (
          <p style={styles.emptyHint}>Start a conversation…</p>
        )}
        {messages.map((msg, i) => (
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
                <Markdown remarkPlugins={[remarkGfm]}>{msg.content}</Markdown>
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
              <p style={styles.bubbleText}>
                {msg.content}
              </p>
            )}
          </div>
        ))}
        {error && <p style={styles.errorText}>Error: {error}</p>}
        <div ref={bottomRef} />
      </div>

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
        <div style={styles.inputRow}>
          <button
            onClick={() => fileInputRef.current?.click()}
            style={styles.attachBtn}
            title="Attach file"
            aria-label="Attach file"
          >
            📎
          </button>
          <textarea
            ref={inputRef}
            style={styles.textarea}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Message autowiki… (Enter to send, Shift+Enter for newline)"
            rows={3}
          />
          <button
            onClick={sendMessage}
            disabled={sending || !input.trim() || pendingAttachments.some((a) => a.uploading)}
            style={styles.sendBtn}
          >
            {sending ? "…" : "Send"}
          </button>
        </div>
      </div>
    </div>
  );
}

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
    whiteSpace: "pre-wrap",
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
    padding: "1rem 1.5rem",
    borderTop: "1px solid #e5e5e5",
    background: "#fff",
  },
  inputRow: {
    display: "flex",
    gap: "0.75rem",
    alignItems: "flex-end",
  },
  textarea: {
    flex: 1,
    resize: "none",
    border: "1px solid #ddd",
    borderRadius: "8px",
    padding: "0.6rem 0.85rem",
    fontSize: "0.95rem",
    fontFamily: "inherit",
    lineHeight: 1.5,
    outline: "none",
    color: "#111",
    background: "#fff",
  },
  sendBtn: {
    padding: "0.6rem 1.2rem",
    background: "#111",
    color: "#fff",
    border: "none",
    borderRadius: "8px",
    cursor: "pointer",
    fontSize: "0.95rem",
    fontWeight: 500,
    height: "fit-content",
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
  attachBtn: {
    background: "none",
    border: "none",
    cursor: "pointer",
    fontSize: "1.1rem",
    padding: "0.4rem",
    borderRadius: "6px",
    height: "fit-content",
    alignSelf: "flex-end",
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
};
