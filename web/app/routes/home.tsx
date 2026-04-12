import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import type { Route } from "./+types/home";

export function meta({}: Route.MetaArgs) {
  return [{ title: "autowiki" }];
}

export default function Home() {
  const navigate = useNavigate();
  const [email, setEmail] = useState<string | null>(null);

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
        if (data?.email) setEmail(data.email);
      })
      .catch(() => navigate("/login", { replace: true }));
  }, [navigate]);

  if (!email) return null;

  return (
    <main style={{ padding: "2rem", fontFamily: "Inter, sans-serif" }}>
      <p>Signed in as {email}</p>
    </main>
  );
}
