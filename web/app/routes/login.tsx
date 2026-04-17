import { useEffect } from "react";
import { useNavigate } from "react-router";
import type { Route } from "./+types/login";

export function meta({}: Route.MetaArgs) {
  return [{ title: "Sign in — autowiki" }];
}

export default function Login() {
  const navigate = useNavigate();
  const params = new URLSearchParams(
    typeof window !== "undefined" ? window.location.search : ""
  );
  const error = params.get("error");

  useEffect(() => {
    // If already authenticated, skip the login page.
    fetch("/api/auth/me")
      .then((r) => {
        if (r.ok) navigate("/", { replace: true });
      })
      .catch(() => {});
  }, [navigate]);

  return (
    <main style={styles.container}>
      <h1 style={styles.title}>auto<span style={{ color: "#1d4ed8" }}>wiki</span></h1>
      <p style={styles.subtitle}>Your self-maintaining knowledge base</p>

      {error === "unauthorized" && (
        <p style={styles.error}>
          That Google account is not allowed. Try a different account.
        </p>
      )}

      <a href="/api/auth/login" style={styles.button}>
        Sign in with Google
      </a>
    </main>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    minHeight: "100vh",
    gap: "1rem",
    fontFamily: "Inter, sans-serif",
  },
  title: {
    fontSize: "2rem",
    fontWeight: 700,
    margin: 0,
  },
  subtitle: {
    color: "#666",
    margin: 0,
  },
  error: {
    color: "#c00",
    fontSize: "0.9rem",
  },
  button: {
    marginTop: "1rem",
    padding: "0.75rem 1.5rem",
    backgroundColor: "#4285F4",
    color: "#fff",
    borderRadius: "4px",
    textDecoration: "none",
    fontWeight: 500,
  },
};
