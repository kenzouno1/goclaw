import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Plus, RefreshCw, ShieldAlert, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { formatCost, formatDate, formatTokens } from "@/lib/format";
import { useAgents } from "@/pages/agents/hooks/use-agents";
import { useProviders } from "@/pages/providers/hooks/use-providers";
import { useUsageCaps } from "../hooks/use-usage-caps";
import type { UsageCapPolicy, UsageCapUtilization } from "@/types/usage-caps";

const ALL = "__all__";

export function UsageCapsPanel() {
  const { t } = useTranslation("usage");
  const { agents } = useAgents();
  const { providers } = useProviders();
  const { utilization, events, refreshing, refresh, createPolicy, deletePolicy } = useUsageCaps();
  const [windowValue, setWindowValue] = useState<UsageCapPolicy["window"]>("day");
  const [agentId, setAgentId] = useState(ALL);
  const [providerId, setProviderId] = useState(ALL);
  const [modelId, setModelId] = useState("");
  const [maxTokens, setMaxTokens] = useState("");
  const [maxCost, setMaxCost] = useState("");
  const [saving, setSaving] = useState(false);

  const provider = useMemo(() => providers.find((p) => p.id === providerId), [providerId, providers]);
  const blockedEvents = events.filter((event) => event.decision === "block");

  const onSubmit = async () => {
    const tokens = Number(maxTokens);
    const cost = Number(maxCost);
    if ((!Number.isFinite(tokens) || tokens <= 0) && (!Number.isFinite(cost) || cost <= 0)) return;
    setSaving(true);
    try {
      await createPolicy({
        window: windowValue,
        agent_id: agentId === ALL ? undefined : agentId,
        provider_id: providerId === ALL ? undefined : providerId,
        provider_type: provider?.provider_type,
        model_id: modelId.trim() || undefined,
        max_tokens: Number.isFinite(tokens) && tokens > 0 ? Math.floor(tokens) : undefined,
        max_cost_usd: Number.isFinite(cost) && cost > 0 ? cost : undefined,
        enabled: true,
      });
      setMaxTokens("");
      setMaxCost("");
      setModelId("");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="space-y-4 rounded-md border p-3 sm:p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h3 className="text-sm font-semibold">{t("caps.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("caps.description")}</p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing} className="gap-1 self-start sm:self-auto">
          <RefreshCw className={`h-3.5 w-3.5${refreshing ? " animate-spin" : ""}`} />
          {t("common:refresh", "Refresh")}
        </Button>
      </div>

      <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-6">
        <Field label={t("caps.window")}>
          <Select value={windowValue} onValueChange={(value) => setWindowValue(value as UsageCapPolicy["window"])}>
            <SelectTrigger className="w-full text-base md:text-sm"><SelectValue /></SelectTrigger>
            <SelectContent>
              {["hour", "day", "week", "month"].map((value) => <SelectItem key={value} value={value}>{t(`caps.windows.${value}`)}</SelectItem>)}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t("caps.agent")}>
          <Select value={agentId} onValueChange={setAgentId}>
            <SelectTrigger className="w-full text-base md:text-sm"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t("caps.allAgents")}</SelectItem>
              {agents.map((a) => <SelectItem key={a.id} value={a.id}>{a.display_name || a.agent_key || a.id}</SelectItem>)}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t("caps.provider")}>
          <Select value={providerId} onValueChange={setProviderId}>
            <SelectTrigger className="w-full text-base md:text-sm"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t("caps.allProviders")}</SelectItem>
              {providers.map((p) => <SelectItem key={p.id} value={p.id}>{p.display_name || p.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t("caps.model")}>
          <Input value={modelId} onChange={(e) => setModelId(e.target.value)} placeholder="openai/gpt-4o-mini" className="text-base md:text-sm" />
        </Field>
        <Field label={t("caps.maxTokens")}>
          <Input value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} inputMode="numeric" placeholder="500000" className="text-base md:text-sm" />
        </Field>
        <Field label={t("caps.maxCost")}>
          <div className="flex gap-2">
            <Input value={maxCost} onChange={(e) => setMaxCost(e.target.value)} inputMode="decimal" placeholder="25" className="text-base md:text-sm" />
            <Button type="button" size="icon" onClick={() => void onSubmit()} disabled={saving} aria-label={t("caps.create")}>
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </Field>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-sm">
          <thead>
            <tr className="border-b bg-muted/40">
              <th className="px-3 py-2 text-left font-medium">{t("caps.scope")}</th>
              <th className="px-3 py-2 text-left font-medium">{t("caps.window")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("caps.tokens")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("caps.cost")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("columns.status")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("columns.actions", "Actions")}</th>
            </tr>
          </thead>
          <tbody>
            {utilization.length === 0 ? (
              <tr><td colSpan={6} className="px-3 py-6 text-center text-muted-foreground">{t("caps.empty")}</td></tr>
            ) : utilization.map((row) => (
              <UsageCapRow key={row.policy.id} row={row} onDelete={() => void deletePolicy(row.policy.id)} />
            ))}
          </tbody>
        </table>
      </div>

      {blockedEvents.length > 0 ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium"><ShieldAlert className="h-4 w-4 text-destructive" />{t("caps.recentBlocks")}</div>
          <div className="grid gap-2 sm:grid-cols-2">
            {blockedEvents.slice(0, 4).map((event) => (
              <div key={event.id} className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs">
                <div className="font-medium">{event.reason || t("caps.blocked")}</div>
                <div className="text-muted-foreground">{formatDate(event.created_at)} · {formatTokens(event.estimated_tokens)} · {formatCost(event.estimated_cost_micros / 1_000_000)}</div>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </section>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <div className="space-y-1.5"><Label className="text-xs">{label}</Label>{children}</div>;
}

function UsageCapRow({ row, onDelete }: { row: UsageCapUtilization; onDelete: () => void }) {
  const { t } = useTranslation("usage");
  const p = row.policy;
  const tokenUsed = row.used_tokens + row.reserved_tokens;
  const costUsed = row.used_cost_micros + row.reserved_cost_micros;
  const tokenPct = p.max_tokens ? Math.min(100, Math.round((tokenUsed / p.max_tokens) * 100)) : 0;
  const costPct = p.max_cost_micros ? Math.min(100, Math.round((costUsed / p.max_cost_micros) * 100)) : 0;
  return (
    <tr className="border-b last:border-0">
      <td className="px-3 py-2">
        <div className="font-medium">{p.model_id || p.provider_type || t("caps.tenantScope")}</div>
        <div className="text-xs text-muted-foreground">{p.agent_id ? t("caps.agentScoped") : t("caps.tenantScoped")}</div>
      </td>
      <td className="px-3 py-2"><Badge variant="outline">{t(`caps.windows.${p.window}`)}</Badge></td>
      <td className="px-3 py-2 text-right">{p.max_tokens ? `${formatTokens(tokenUsed)} / ${formatTokens(p.max_tokens)} (${tokenPct}%)` : "—"}</td>
      <td className="px-3 py-2 text-right">{p.max_cost_micros ? `${formatCost(costUsed / 1_000_000)} / ${formatCost(p.max_cost_micros / 1_000_000)} (${costPct}%)` : "—"}</td>
      <td className="px-3 py-2 text-right"><Badge variant={p.enabled ? "default" : "secondary"}>{p.enabled ? t("caps.enabled") : t("caps.disabled")}</Badge></td>
      <td className="px-3 py-2 text-right">
        <Button type="button" variant="ghost" size="icon" onClick={onDelete} aria-label={t("caps.delete")}><Trash2 className="h-4 w-4" /></Button>
      </td>
    </tr>
  );
}
