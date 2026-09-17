// Login — вход в защищённый Web UI (Ф-3). Показывается, когда сервер запущен
// с AI_WEB_PASSWORD и у клиента нет валидной httpOnly-сессии. После успеха
// фронт получает CSRF-токен (см. Api.login/setCSRF) и перезагружает данные.

import { useState } from "react";
import { login } from "@/Api";
import "./styles.scss";

export function Login({
  base,
  onLoggedIn,
}: {
  base: string;
  onLoggedIn: () => void;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const st = await login(base, password);
      if (!st.ok) {
        setError("Неверный пароль");
        return;
      }
      onLoggedIn();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h1>Вход</h1>
        <p>Web UI доступен только по паролю (AI_WEB_PASSWORD).</p>
        <input
          type="password"
          placeholder="Пароль"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoFocus
          disabled={busy}
          autoComplete="current-password"
        />
        {error && <p className="err">{error}</p>}
        <button className="btn primary" disabled={busy || !password}>
          {busy ? "Вхожу…" : "Войти"}
        </button>
      </form>
    </div>
  );
}

function fmtErr(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  return String(err ?? "Ошибка");
}