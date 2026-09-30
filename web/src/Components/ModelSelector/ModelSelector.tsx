import React, { useCallback, useEffect, useMemo, useState } from "react";
import { getProviders, type ProvidersResponse, selectProvider } from "../../Api/Api";
import "./ModelSelector.scss";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: embed same-origin.

// Значение option — индекс в плоском списке, а не "провайдер:модель": имена
// моделей содержат двоеточия (qwen3-coder:30b) и слэши (/models/T-pro-it-1.0),
// поэтому разбор строки по ":" молча ломал бы выбор модели.
interface Choice {
  provider: string;
  model: string;
  large: string;
}

const NONE = "—";

// Переключатель модели: два селекта (провайдер с моделями + необязательная
// большая модель для «тяжёлых» агентов) и кнопка сброса к переменным
// окружения. Ошибки сервера показываются пользователю и откатывают выбор —
// раньше catch(() => {}) глушил их, и UI выглядел работающим, хотя выбор не
// применялся.
export const ModelSelector: React.FC = () => {
  const [data, setData] = useState<ProvidersResponse | null>(null);
  const [choice, setChoice] = useState<Choice | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const apply = useCallback((res: ProvidersResponse) => {
    setData(res);
    setChoice({
      provider: res.current_provider,
      model: res.current_model,
      large: res.current_large_model,
    });
  }, []);

  useEffect(() => {
    getProviders(BASE)
      .then((res) => {
        apply(res);
        if (res.error) {
          setError(res.error);
        }
      })
      .catch((err: Error) => setError(err.message || "Ошибка загрузки провайдеров"))
      .finally(() => setLoading(false));
  }, [apply]);

  // Плоский список вариантов для <select>: основная модель каждого провайдера.
  const options = useMemo(() => {
    if (!data) {
      return [] as Choice[];
    }
    return data.providers.flatMap((p) => p.models.map((m) => ({ provider: p.name, model: m, large: "" })));
  }, [data]);

  // Индекс в плоском списке. Пустая строка — провайдер не задан в окружении
  // (LLM_PROVIDER пуст): показываем плейсхолдер, а не молча бьющееся пустое
  // поле. Разбор значения в onChange тоже должен в этом случае выйти.
  const selectedIndex = useMemo(() => {
    if (!choice || !choice.provider) {
      return "";
    }
    const i = options.findIndex((o) => o.provider === choice.provider && o.model === choice.model);
    return i >= 0 ? String(i) : "";
  }, [options, choice]);

  // Модели целевого провайдера — считаются по его имени, а не по choice:
  // в onChange селект ещё отражает ПРЕДЫДУЩЕГО провайдера.
  const largeModelsOf = useCallback(
    (provider: string): string[] => data?.providers.find((p) => p.name === provider)?.models ?? [],
    [data],
  );

  const commit = useCallback((next: Choice) => {
    const prev = choice;
    setChoice(next);
    setBusy(true);
    setError("");
    selectProvider(BASE, {
      provider: next.provider,
      model: next.model,
      large_model: next.large,
    })
      .then((res) => {
        // Сервер мог нормализовать выбор (пустая модель → default_model) —
        // переносим фактическое, чтобы селекты не расходились с бэкендом.
        setChoice({ provider: res.provider, model: res.model, large: res.large_model });
        if (res.applies_to_running && res.message) {
          setError(res.message);
        }
      })
      .catch((err: Error) => {
        setError(err.message || "Не удалось применить выбор модели");
        // Откат: сервер выбор не принял — возвращаем то, что реально работает.
        if (prev) {
          setChoice(prev);
        }
      })
      .finally(() => setBusy(false));
  }, [choice]);

  const reset = useCallback(() => {
    setBusy(true);
    setError("");
    selectProvider(BASE, { provider: "", model: "", reset: true })
      .then(() => getProviders(BASE))
      .then(apply)
      .catch((err: Error) => setError(err.message || "Не удалось сбросить выбор модели"))
      .finally(() => setBusy(false));
  }, [apply]);

  if (loading) {
    return <div className="model-selector">Модель…</div>;
  }
  if (!data || data.providers.length === 0) {
    return error ? <div className="model-selector error" title={error}>LLM: нет конфигурации</div> : null;
  }

  return (
    <div className="model-selector" title={error || undefined}>
      <span className="model-selector-label">Модель</span>
      <select
        value={selectedIndex}
        disabled={busy}
        onChange={(e) => {
          if (e.target.value === "") {
            return; // плейсхолдер «провайдер не задан» — менять нечего
          }
          const picked = options[Number(e.target.value)];
          if (!picked) {
            return;
          }
          // При смене провайдера старая крупная модель может быть недоступна —
          // сбрасываем её, иначе сервер отклонит выбор целиком.
          const keepLarge = largeModelsOf(picked.provider).includes(choice?.large ?? "") ? (choice?.large ?? "") : "";
          commit({ ...picked, large: keepLarge });
        }}
        className="model-selector-select"
      >
        {selectedIndex === "" && (
          <option value="" disabled>
            провайдер не задан
          </option>
        )}
        {data.providers.map((p) => (
          <optgroup key={p.name} label={p.name}>
            {p.models.map((m) => {
              const i = options.findIndex((o) => o.provider === p.name && o.model === m);
              return (
                <option key={`${p.name}:${m}`} value={String(i)}>
                  {m}
                  {m === p.default_model ? " (по умолчанию)" : ""}
                </option>
              );
            })}
          </optgroup>
        ))}
      </select>
      <select
        value={choice?.large || NONE}
        disabled={busy}
        onChange={(e) => {
          if (choice) {
            commit({ ...choice, large: e.target.value === NONE ? "" : e.target.value });
          }
        }}
        className="model-selector-select"
        title="Большая модель для лидов, архитектора и ревьюера (пусто — все агенты на основной)"
      >
        <option value={NONE}>крупная: нет</option>
        {largeModelsOf(choice?.provider ?? "").map((m) => (
          <option key={m} value={m}>
            крупная: {m}
          </option>
        ))}
      </select>
      {data.override && (
        <button className="model-selector-reset" disabled={busy} onClick={reset} title="Вернуть выбор из переменных окружения">
          сброс
        </button>
      )}
      {error && <span className="model-selector-msg">{error}</span>}
    </div>
  );
};
