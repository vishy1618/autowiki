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

interface Message {
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
  vaultChanges?: VaultChange[];
}

export default function Home() {
  const navigate = useNavigate();
  const [ready, setReady] = useState(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

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

  async function sendMessage() {
    const text = input.trim();
    if (!text || sending) return;

    setInput("");
    setError(null);
    setSending(true);
    inputRef.current?.focus();

    setMessages((prev) => [
      ...prev,
      { role: "user", content: text },
      { role: "assistant", content: "", streaming: true },
    ]);

    try {
      const body = new URLSearchParams({ message: text });
      const resp = await fetch("/api/chat", {
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
            if (currentEvent === "delta") {
              try {
                const { text } = JSON.parse(data) as { text: string };
                setMessages((prev) => {
                  const next = [...prev];
                  const last = next[next.length - 1];
                  if (last?.role === "assistant") {
                    next[next.length - 1] = {
                      ...last,
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
    <div style={styles.layout}>
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
            {msg.role === "assistant" ? (
              <div style={styles.bubbleText}>
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
          disabled={sending || !input.trim()}
          style={styles.sendBtn}
        >
          {sending ? "…" : "Send"}
        </button>
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
    gap: "0.75rem",
    padding: "1rem 1.5rem",
    borderTop: "1px solid #e5e5e5",
    background: "#fff",
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
