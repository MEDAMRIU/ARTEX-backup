"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 纯前端偏好：不走 /api/settings，直接读写 localStorage。
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("Concurrency must be an integer greater than 0");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("Concurrent worker agent count saved (applies to tasks started afterward)");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("Python interpreter configuration saved");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Automatic Agent traffic binding enabled" : "Automatic Agent traffic linking disabled");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`Failed to save: ${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "noa context compression enabled (applies to subsequent runs)" : "noa context compaction disabled (built-in compaction restored)");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`Failed to save: ${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("Web search configuration saved");
      })
      .catch((e) => {
        toast.error("Failed to save:" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("Brave API Key saved");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("Tavily API Key saved");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "Outbound proxy saved" : "Outbound proxy cleared (using a direct connection)");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "Global proxy saved" : "Global proxy cleared (using a direct connection)");
      })
      .catch((e) => toast.error("Failed to save:" + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`Search test succeeded · ${r.backend} returned ${r.count} results`);
        else toast.error("Search test failed:" + (r.error || "Unknown error"));
      })
      .catch((e) => toast.error("Search test failed:" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{"System settings"}</h1>
        <p className="text-muted-foreground text-sm">{"Global runtime toggles"}</p>
      </div>

      {/* 多列而非 grid：网络搜索卡片比其余高数倍，且高度随所选后端变化（brave/tavily
          的 key 输入是条件渲染）。grid 会按最高的一张撑满整行、在旁边留下大片空白，
          多列则自动按内容高度平衡填充。卡片间距靠 mb 而非 gap——多列布局下
          column-gap 只管列间距，行间距要由子元素自己给。 */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {"Traffic capture"}
            </CardTitle>
            <CardDescription>
              {"When enabled, all HTTP traffic from all Agents is stored through the recording proxy, and Agents receive traffic_search / traffic_get\n              tools and proxy configuration (including proxy instructions in their prompts)."}
              <br />
              {"When disabled (default), no traffic is recorded: Agent"}
              <b>{"will not"}</b>{" receive proxy configuration and traffic tools, and their prompts also "}<b>{"excluding"}</b>{"proxy-related content. Agents are rebuilt immediately when toggled."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "Enabled · Recording traffic and injecting proxy configuration" : "Disabled · No recording or proxy injection"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {"Agent automatic traffic linking"}
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              {"Off by default. When enabled, the report Agent triggered by a new vulnerability checks existing HTTP requests/responses and links the relevant traffic before writing the report."}
              <b>{"Inspecting packets and making additional tool calls increases token usage."}</b>
              <br />
              {"TCP traffic, uncaptured traffic, and traffic with no match can still be reported. This does not affect traffic capture, manual linking, or viewing saved evidence. Applies to the next agent\n              run; disabling immediately rejects new automatic links."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "Enabled · Increases token usage" : "Disabled · Manual linking is still available"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {"Global proxy"}
            </CardTitle>
            <CardDescription>
              {"All Agents’ "}<b>{"Target traffic"}</b>{"route outbound traffic through this proxy (hide source IP / use a jump host). Supports"} <b>http / https / socks5</b>{"; supports "}{" "}
              <code>user:pass</code> {"authentication. Blank = direct connection."}
              <br />
              {"Enabled"}<b>{"Traffic capture"}</b>{", it acts as the recording proxy's"}<b>{"Upstream"}</b>{"(all traffic is still saved to the database before passing through this outbound proxy); when capture is disabled, it is injected directly into\n              Agent bash / WebFetch outbound connections. Independent of the web search proxy and LLM proxy."}
              <br />
              <b>{"Hint"}</b>{": socks5 in"}<b>{"Disable capture"}</b>{"depends on each command-line tool's support for"} <code>ALL_PROXY</code> {"support (curl\n              works; some tools may ignore it). If you mainly use socks5, enable traffic capture: MITM\n              establishes connections itself on this path, so it works reliably without tool configuration."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              {"Proxy URL"}
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder={"socks5://user:pass@host:1080 or http://host:port (blank = direct connection)"}
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                {"Save"}
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "Configured · All target traffic uses this outbound proxy" : "Not configured · Target traffic connects directly to the internet"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              {"Operation constraint injection"}
            </CardTitle>
            <CardDescription>
              {"When enabled, each task's"}<b>{"Operation constraints"}</b>{"(allow/deny entries maintained under ‘Operating constraints’ in the task overview) are appended to the corresponding Agent’s\n              system prompt to define exploration boundaries (such as ‘Test only the current port’ or ‘No brute force’)."}
              <br />
              {"Control injection separately into"} <b>{"Planner (planner)"}</b>{"and"} <b>{"Executor (worker)"}</b>
              {"; both are enabled by default. Changes take effect on the next round without rebuilding agents. When disabled, that agent no longer sees the constraints."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                {"Inject into planner"}{injectPlanner ? " · Enabled" : " · Disabled"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                {"Inject into worker"}{injectWorker ? " · Enabled" : " · Disabled"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              {"Experimental features"}
            </CardTitle>
            <CardDescription>
              {"Experimental mechanisms, disabled by default. They may change agent behavior or affect stability. Enable them after understanding their effects."}
              <br />
              <b>{"noa context compaction"}</b>{": the model proactively compacts long conversation history (norma v0.4.0). When enabled, all four Agent types integrated into the platform ("}
              <b>{"Planner / Worker / Main agent / Chat"}</b>{") use noa to manage context in place of built-in compaction.\n              Original compacted content is archived in the task workspace for reference. Changes apply immediately to subsequent runs without rebuilding Agents;\n              disabling this immediately restores built-in compaction."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              {"noa context compaction"}{noaCompaction ? " · Enabled" : " · Disabled"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              {"Web search"}
            </CardTitle>
            <CardDescription>
              {"This is the web search"}<b>{"Master toggle + source settings"}</b>{". Once enabled, "}<b>{"Each Agent's configuration"}</b>{"to enable it individually"}
              <b>web_search</b>{"(returns titles/links/summaries only; WebFetch retrieves full content). Web search "}<b>{"does not use"}</b>
              {"recording proxy, independently of traffic capture."}
              <br />
              {"Available sources: "} <b>DuckDuckGo（ddgs）</b>{"(no key required),"}<b>{"Brave (free tier)"}</b>{"(requires Brave API Key), "}{" "}
              <b>Tavily</b>{"(requires a Tavily API Key) or"} <b>DeepSeek</b>{"(reuses the current LLM configuration). When the master toggle is off, individual\n              agents cannot enable web search."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "Global switch enabled · Can be enabled separately in each Agent configuration" : "Disabled · Web search unavailable to all Agents"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">{"Search source"}</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder={"Select source"} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">{"DuckDuckGo (ddgs · free, no key required)"}</SelectItem>
                    <SelectItem value="brave-free">{"Brave (free tier · Key required)"}</SelectItem>
                    <SelectItem value="tavily">{"Tavily (key required)"}</SelectItem>
                    <SelectItem value="deepseek">{"DeepSeek (official)"}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">{"Official DeepSeek web search"}</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {"This source directly reuses "}<b>{"The currently active LLM configuration"}</b>{". Therefore it"}
                  <b>{"Supports official DeepSeek models only"}</b>{", and that configuration"}<b>{"must use the anthropic protocol"}</b>
                  {"—DeepSeek’s OpenAI-protocol endpoint does not support server-side search. This source may stop working after switching LLM configurations."}
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {"Unlike other sources, searches are performed by"} <b>{"Executed on DeepSeek servers"}</b>{": each search uses an additional model call (incurring token\n                  costs); search requests "}<b>{"bypass the outbound proxy above"}</b>{", and also"}<b>{"are not included in traffic logs"}</b>{"; returned results"}<b>{"Titles and links only"}</b>
                  {"(no summaries); WebFetch retrieves full content when needed."}
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {"You must confirm that these conditions are met; the system does not block requests. Use “Test search” below to run a search and verify."}
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">{"Configured"}</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "Configured (leave blank to keep)" : "Enter Brave API Key"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    {"Save"}
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    {"Brave selected but no key configured. The search tool remains disabled until the key is saved."}
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  {"Free tier includes about 2,000 requests/month. Get a key at https://brave.com/search/api/."}
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">{"Configured"}</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "Configured (leave blank to keep)" : "Enter Tavily API Key (tvly-…)"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    {"Save"}
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    {"Tavily selected but no key configured. The search tool remains disabled until the key is saved."}
                  </p>
                )}
                <p className="text-muted-foreground text-xs">{"Register at https://tavily.com to get an API Key."}</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  {"Outbound proxy (optional)"}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder={"http://host:port or socks5://host:port (blank = direct connection)"}
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    {"Save"}
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  {"A separate outbound proxy used only to access search endpoints (VPN/SOCKS, etc.). Independent of the traffic-recording MITM proxy; use it when the network is unreachable."}
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  {"Run a real search for ‘test’ using the current configuration (source + proxy + key) to verify availability."}
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "Testing…" : "Test search"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {"Custom scripts · Python interpreter"}
            </CardTitle>
            <CardDescription>
              {"Custom"} <b>script</b> {" tools use it to run Python. Automatically detected at startup (python3 preferred); enter an absolute path to a venv /\n              specific version here, or leave empty for automatic detection at runtime."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder={"/usr/bin/python3 (blank = auto-detect)"}
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                {"Detect again"}
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                {"Save"}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              {"Worker concurrency · Work Agent count"}
            </CardTitle>
            <CardDescription>
              {"Number of worker agents running concurrently per task (default 3). Higher values mean more concurrent probes and higher usage. After changes,"}
              <b>{"Applies to tasks started afterward"}</b>{"; running tasks are unaffected."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                {"Save"}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              {"Conversation send shortcut"}
            </CardTitle>
            <CardDescription>
              {"Shared by the chat page and main agent session input in task details. Takes effect immediately when selected; no save needed."}
              <br />
              {"This preference "}<b>{"Stored only in this browser"}</b>{", is not synced with your account, and must be set again after switching browsers or clearing site data."}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              {"Send mode"}
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
