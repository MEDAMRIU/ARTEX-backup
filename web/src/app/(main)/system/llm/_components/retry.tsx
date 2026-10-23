"use client";

// LLM 重试配置的共用件：五层重试各自的「次数 + 间隔」。
//
// 五层从内到外：建连(SDK) → 空响应(SDK) → 同 provider 安全窗口 → 轮询熔断 → 意图重跑。
// 前三层跟着端点走，所以每个模型配置都能覆盖全局默认；后两层是进程级的，只有全局一份。
//
// 所有输入都遵循同一套「留空 = 不配置」语义，与后端 db.RetryRule 一致：
//   次数   空/0 = 用内置默认 | -1 = 关闭这层重试 | >0 = 用这个次数
//   间隔   空/0 = 用这层原本的指数退避 | >0 = 改用这个固定毫秒间隔

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** 这层重试发生在哪、由谁执行 */
  where: string;
  /** 什么样的错误会走到这层——具体到状态码，别让人猜 */
  trigger: string;
  /** 长得像但【不】走这层的错误，省得填了没反应还以为是 bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 次数留空时的默认值，用于占位符 */
  defAttempts: number;
  /** 间隔留空时的默认策略，用于占位符 */
  defInterval: string;
  /** 次数填 -1 的含义 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connection retries",
    where: "SDK · Before HTTP 200",
    trigger:
      "Connection failures or failures before receiving 200: connection resets, read/write timeouts, DNS failures and other network errors, plus HTTP 408, 429, 500, 502, 503, and 504.",
    skips: "Other status codes (400 / 401 / 403 / 404 / 413 / 422, etc.) are definitive rejections. Retrying produces the same failure, so they are raised immediately.",
    desc: "Resend the same request unchanged. Once streaming starts (HTTP 200 received), interruptions are no longer handled by this layer.",
    attemptsLabel: "Retry count",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = no retries; propagate failure immediately",
  },
  empty: {
    title: "Retry empty responses",
    where: "SDK · openai format only",
    trigger:
      "HTTP 200 with a normal stop finish_reason, but no content blocks in the entire response. Empty gateway frames, missing thinking fields, or sampling glitches can cause this.",
    skips: "Excludes empty content caused by max_tokens truncation (increase the output limit to resolve this; resending will hit the same limit).",
    desc: "Resends the entire prompt, which is expensive with long contexts. Keep the retry count low.",
    attemptsLabel: "Retry count",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = return empty responses unchanged",
  },
  stream: {
    title: "Retry within the same provider’s safe window",
    where: "This project · Before output is delivered",
    trigger:
      "Problems after the stream is established (HTTP 200 received): connection drops, provider overloaded errors, or in-stream 429 / 5xx error events, before any token is delivered to the caller.",
    skips:
      "Do not retry quota exhaustion (402 / insufficient_quota; handled by configuration rotation), oversized context (413 / context length; handled by compaction), or deterministic rejections: 400 / 401 / 403 / 404 / 422.",
    desc: "Replays the same request using the same configuration. Since no output has been delivered, replaying does not duplicate model output or tool execution.",
    attemptsLabel: "Retry count",
    defAttempts: 2,
    defInterval: "Exponential 0.5s→1s (capped at 4s)",
    offHint: "-1 = hand stream interruptions directly to the outer intent rerun layer",
  },
  breaker: {
    title: "Rotation circuit breaker",
    where: "This project · One global instance per process",
    trigger:
      "Transient failures (429, 5xx, network errors) trip the circuit breaker after consecutive failures reach the threshold; deterministic failures such as insufficient balance (402), invalid keys (401 / 403), or missing models (404) trip it immediately on the first failure.",
    skips: "A single success resets the count, so occasional failures do not gradually trip the circuit breaker.",
    desc: "Tripping the circuit breaker starts a cooldown. Rotation skips this configuration during cooldown. State is persisted in the database and survives restarts.",
    attemptsLabel: "Consecutive failures before circuit breaker trips",
    defAttempts: 3,
    defInterval: "1min→5min→30min tiers",
    offHint: "-1 = transient failures never trip the breaker (deterministic failures still do)",
  },
  intent: {
    title: "Intent reruns",
    where: "This project · One global instance per process",
    trigger:
      "If earlier layers cannot recover, the worker ends with model_error: all inner retries are exhausted, or the stream disconnects after output delivery starts (when replaying is unsafe and the entire run must restart).",
    skips: "Quota exhaustion is handled by rotating configurations and is not retried here. Yield immediately when the task is paused, terminated, or wrapping up, without waiting through backoff.",
    desc: "Reruns the entire intent from the start. This is the outermost layer, so each rerun multiplies the attempts in the inner layers again.",
    attemptsLabel: "Rerun count",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = no rerun; mark the intent blocked directly",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** 毫秒的人话，只用于在输入框旁边回显，免得数零。 */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** 受控数字输入：空串 ↔ 0，中间态（"-"、"1e"）原样留在本地，不打扰父级。 */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // 父级换了一整套值（读取到策略、切换配置）时跟上；自己敲字时不会走到这里，
  // 因为那时 value 已经等于本地文本 parse 后的结果。
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** 一层重试的两个旋钮。idPrefix 用来在同一页出现多次时保住 label 的 htmlFor。 */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = 配置抽屉里的紧凑版：省掉展开说明，只留「什么错误会走到这层」这一句 */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* 哪些错误会走到这层，具体到状态码——填了旋钮却看不到效果，多半是错误压根不落在这层。 */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">{"Trigger"}</span>：{meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">{"Bypasses this layer"}</span>：{meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            {"Interval ms"}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder={"Default backoff"}
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">{"Leave blank to use the default;"}{meta.offHint}。</p>}
    </div>
  );
}

/** 模型配置抽屉里的三层覆盖（跟着端点走的那三层）。 */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">{"Retry overrides"}</Label>
        <p className="text-muted-foreground text-xs">
          {"Applies only to this configuration, overriding global defaults in ‘Retries and backoff’. Leave a field empty to use the global value; set a count to -1 to disable retries at that layer;\n          setting an interval replaces exponential backoff with a fixed interval. Circuit breaking and intent reruns are process-wide and can only be adjusted on the global page."}
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 「重试与退避」tab：五层的全局默认值。 */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Failed to read retry policy: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // 后端会把越界值夹回区间并回传，直接用回传值刷新，所见即所存。
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved; effective immediately (the current call still uses the previous parameters)");
    } catch (e) {
      toast.error(`Failed to save: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> {"Loading retry policy…"}
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        {"A failed model call passes through five retry layers, from inner to outer:"}
        <span className="text-foreground"> {"Connection → Empty response → Same-provider safe window → Rotation circuit breaker → Intent rerun"}</span>
        {". Outer layers run only after inner layers are exhausted, so the counts are "}
        <span className="text-foreground">{"multiplied"}</span>
        {"—maxing out every layer can burn dozens of requests on a single glitch.\n        Leave all fields blank to keep current defaults and the behavior used before this page existed. The first three layers can be overridden per model configuration."}
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          {"Save"}
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          {"Reset all to defaults"}
        </Button>
      </div>
    </div>
  );
}
