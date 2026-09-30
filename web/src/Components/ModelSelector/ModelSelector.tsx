import React, { useEffect, useState } from "react";
import { getProviders, selectProvider } from "../../Api/Api";

interface Props {
  onModelChange?: (provider: string, model: string) => void;
}

export const ModelSelector: React.FC<Props> = ({ onModelChange }) => {
  const [data, setData] = useState<Awaited<ReturnType<typeof getProviders>> | null>(null);
  const [selectedProvider, setSelectedProvider] = useState("");
  const [selectedModel, setSelectedModel] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    getProviders("")
      .then((res) => {
        setData(res);
        const provider = res.current || res.providers[0]?.name || "";
        const model = res.model || res.providers[0]?.default_model || "";
        setSelectedProvider(provider);
        setSelectedModel(model);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message || "Ошибка загрузки провайдеров");
        setLoading(false);
      });
  }, []);

  if (loading) return <div className="model-selector">Загрузка...</div>;
  if (error) return <div className="model-selector error">{error}</div>;
  if (!data || data.providers.length === 0) return null;

  return (
    <div className="model-selector">
      <select
        value={`${selectedProvider}:${selectedModel}`}
        onChange={(e) => {
          const [provider = "", model = ""] = e.target.value.split(":");
          setSelectedProvider(provider);
          setSelectedModel(model);
          selectProvider("", { provider, model }).catch(() => {});
          onModelChange?.(provider, model);
        }}
        className="model-selector-select"
      >
        {data.providers.map((p) => (
          <optgroup key={p.name} label={p.name}>
            {p.models.map((m) => (
              <option key={`${p.name}:${m}`} value={`${p.name}:${m}`}>
                {m}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </div>
  );
};
