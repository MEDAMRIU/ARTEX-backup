"use client";

import * as React from "react";

import {
  ArrowDownWideNarrowIcon,
  ArrowUpNarrowWideIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  ListChecksIcon,
  Loader2Icon,
  RadioTowerIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { HttpCodeBlock } from "@/components/http-code-block";
import { LinkTrafficDialog } from "@/components/link-traffic-dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { TrafficDetail, TrafficExchange, TrafficHost, TrafficResp } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("en-US", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fmtBytes(n: number) {
  if (n <= 0) return "0 B";
  const units = ["B", "KB", "MB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

function statusTone(status: number) {
  if (status >= 500) return "text-red-500";
  if (status >= 400) return "text-amber-500";
  if (status >= 300) return "text-blue-500";
  if (status >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}

function MethodBadge({ method }: { method: string }) {
  return <Badge className="shrink-0 font-mono">{method}</Badge>;
}

// Older captures may predate Host persistence because net/http keeps Host
// outside Request.Header. Fill it for display while newly recorded traffic is
// fixed at the recorder layer as well.
function requestWithHost(raw: string, exchange: TrafficExchange): string {
  if (!raw.trim() || /^host\s*:/im.test(raw)) return raw;
  let host = exchange.host;
  try {
    host = new URL(exchange.url).host || host;
  } catch {
    // Relative or legacy URLs fall back to the indexed host.
  }
  const newline = raw.includes("\r\n") ? "\r\n" : "\n";
  const firstLineEnd = raw.indexOf(newline);
  if (firstLineEnd < 0) return `${raw}${newline}Host: ${host}`;
  return `${raw.slice(0, firstLineEnd + newline.length)}Host: ${host}${newline}${raw.slice(firstLineEnd + newline.length)}`;
}

// Fixed method set (server filters exact-match); avoids deriving options from a
// single page, which would only ever list the methods on that page.
const METHODS = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];
const PAGE_SIZES = [25, 50, 100, 200];
type HostCountSortDirection = "asc" | "desc";

export default function TrafficPage() {
  const [selectedFlows, setSelectedFlows] = React.useState<Set<string>>(() => new Set());
  const [linking, setLinking] = React.useState(false);
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [host, setHost] = React.useState(""); // raw host input
  const [hostQ, setHostQ] = React.useState(""); // debounced → server
  const [query, setQuery] = React.useState(""); // raw free-text input
  const [queryQ, setQueryQ] = React.useState(""); // debounced → server
  const [method, setMethod] = React.useState("all");

  const [traffic, setTraffic] = React.useState<TrafficResp | null>(null);
  const [selected, setSelected] = React.useState<TrafficExchange | null>(null);
  const [detail, setDetail] = React.useState<TrafficDetail | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);

  const [hosts, setHosts] = React.useState<TrafficHost[]>([]); // target picker
  const [selectedHosts, setSelectedHosts] = React.useState<string[]>([]); // checked in picker
  const [pickerOpen, setPickerOpen] = React.useState(false);
  const [hostCountSortDirection, setHostCountSortDirection] = React.useState<HostCountSortDirection>("desc");

  const [deleteMode, setDeleteMode] = React.useState<"filter" | "selected" | null>(null); // null = dialog closed
  const [deleting, setDeleting] = React.useState(false);
  const [reloadTick, setReloadTick] = React.useState(0); // manual refetch trigger

  // Debounce both filters so we don't refetch on every keystroke.
  React.useEffect(() => {
    const t = setTimeout(() => setHostQ(host.trim()), 300);
    return () => clearTimeout(t);
  }, [host]);
  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // Any filter/size change resets to the first page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: these values intentionally trigger a page reset.
  React.useEffect(() => {
    setPage(0);
  }, [hostQ, queryQ, method, size]);

  // Load the current page. Auto-refresh only on page 0 (newest) so paging back
  // through history isn't yanked out from under the user.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadTick is an explicit manual-refetch trigger.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .traffic(page, size, hostQ, method, queryQ)
        .then((r) => {
          if (alive) setTraffic(r);
        })
        .catch(() => {
          // Keep the last successful snapshot during transient refresh failures.
        });
      api
        .trafficHosts()
        .then((r) => {
          if (alive) setHosts(r.hosts ?? []);
        })
        .catch(() => {
          // Keep the last successful host list during transient refresh failures.
        });
    };
    load();
    const t = setInterval(() => {
      if (page !== 0) return; // only auto-refresh the newest page
      load();
    }, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [page, size, hostQ, method, queryQ, reloadTick]);

  // Delete traffic for the current host filter (substring) or the checked
  // hosts (exact batch), then refetch.
  const allSelected = hosts.length > 0 && hosts.every((h) => selectedHosts.includes(h.host));
  const sortedHosts = React.useMemo(
    () =>
      [...hosts].sort((a, b) => {
        const countOrder = hostCountSortDirection === "asc" ? a.count - b.count : b.count - a.count;
        return countOrder || a.host.localeCompare(b.host);
      }),
    [hosts, hostCountSortDirection],
  );

  const confirmDelete = () => {
    setDeleting(true);
    const p = deleteMode === "selected" ? api.trafficDeleteHosts(selectedHosts) : api.trafficDeleteHost(hostQ);
    p.then(() => {
      setDeleteMode(null);
      setSelected(null);
      setDetail(null);
      if (deleteMode === "selected") {
        setSelectedHosts([]);
        setPickerOpen(false);
      }
      setPage(0);
      setReloadTick((t) => t + 1);
    })
      .catch(() => {
        // Keep the confirmation open so the user can retry a failed deletion.
      })
      .finally(() => setDeleting(false));
  };

  // Lazy-load the raw request/response for the selected exchange.
  React.useEffect(() => {
    if (!selected) {
      setDetail(null);
      return;
    }
    let alive = true;
    setDetailLoading(true);
    setDetail(null);
    api
      .trafficExchange(selected.id)
      .then((d) => {
        if (alive) setDetail(d);
      })
      .catch(() => {
        if (alive) setDetail({ req: "(Unable to load message)", resp: "" });
      })
      .finally(() => {
        if (alive) setDetailLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [selected]);

  const exchanges = React.useMemo(() => traffic?.exchanges ?? [], [traffic]);
  const total = traffic?.total ?? exchanges.length;
  const pageCount = Math.max(1, Math.ceil(total / size));
  const rangeStart = total === 0 ? 0 : page * size + 1;
  const rangeEnd = page * size + exchanges.length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{"Traffic"}</h1>
          <p className="text-muted-foreground text-sm">{"Global recording proxy · All HTTP traffic"}</p>
        </div>
        <div className="flex items-center gap-4 text-sm">
          <span
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium",
              traffic?.enabled
                ? "border-emerald-500/20 bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                : "border-transparent bg-muted text-muted-foreground",
            )}
          >
            <RadioTowerIcon className="size-3.5" />
            {traffic?.enabled ? "Recording" : "Disabled"}
          </span>
          {traffic?.proxy && <span className="font-mono text-xs text-muted-foreground">{traffic.proxy}</span>}
          <span className="text-xs text-muted-foreground">
            {"Total"} <span className="tabular-nums">{traffic?.count ?? 0}</span> {" records"}
          </span>
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
          <PopoverTrigger asChild>
            <Button variant="outline" size="sm" className="h-8">
              <ListChecksIcon className="size-3.5" />
              {selectedHosts.length > 0 ? `Select targets (${selectedHosts.length}）` : "Select target…"}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            className="w-[calc(100vw-2rem)] p-0 data-open:animate-none data-closed:animate-none sm:w-80"
            align="start"
            collisionPadding={16}
          >
            <div className="flex items-center justify-between border-b px-3 py-2">
              <span className="text-xs font-medium text-muted-foreground">{"Bulk delete by target"}</span>
              <div className="flex items-center gap-1">
                {hosts.length > 0 && (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        onClick={() => setHostCountSortDirection((current) => (current === "desc" ? "asc" : "desc"))}
                        aria-label={
                          hostCountSortDirection === "desc"
                            ? "Packet count is descending; click to sort ascending"
                            : "Packet count ascending; click to sort descending"
                        }
                      >
                        {hostCountSortDirection === "desc" ? <ArrowDownWideNarrowIcon /> : <ArrowUpNarrowWideIcon />}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>{"By packet count"}{hostCountSortDirection === "desc" ? "Descending" : "Ascending"}</TooltipContent>
                  </Tooltip>
                )}
                {hosts.length > 0 && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2 text-xs"
                    onClick={() => setSelectedHosts(allSelected ? [] : hosts.map((h) => h.host))}
                  >
                    {allSelected ? "Deselect all" : "Select all"}
                  </Button>
                )}
              </div>
            </div>
            <div className="max-h-64 overflow-y-auto">
              {hosts.length === 0 ? (
                <div className="px-3 py-6 text-center text-xs text-muted-foreground">{"No traffic records yet"}</div>
              ) : (
                sortedHosts.map((h, index) => (
                  <label
                    key={h.host}
                    htmlFor={`traffic-host-${index}`}
                    className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-xs hover:bg-accent"
                  >
                    <Checkbox
                      id={`traffic-host-${index}`}
                      checked={selectedHosts.includes(h.host)}
                      onCheckedChange={() =>
                        setSelectedHosts((prev) =>
                          prev.includes(h.host) ? prev.filter((x) => x !== h.host) : [...prev, h.host],
                        )
                      }
                    />
                    <span className="truncate font-mono">{h.host}</span>
                    <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">{h.count}</span>
                  </label>
                ))
              )}
            </div>
            <div className="border-t p-2">
              <Button
                variant="destructive"
                size="sm"
                className="w-full"
                disabled={selectedHosts.length === 0}
                onClick={() => {
                  setDeleteMode("selected");
                  setPickerOpen(false);
                }}
              >
                {"Delete selected ("}{selectedHosts.length}）
              </Button>
            </div>
          </PopoverContent>
        </Popover>
        <div className="relative w-48">
          <Input placeholder="host…" value={host} onChange={(e) => setHost(e.target.value)} className="h-8" />
        </div>
        <Button
          variant="destructive"
          size="sm"
          className="h-8"
          disabled={!hostQ || deleting}
          title={hostQ ? undefined : "Select a target on the left or enter a host first"}
          onClick={() => setDeleteMode("filter")}
        >
          <Trash2Icon className="size-3.5" />
          {"Delete this target"}
        </Button>
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder={"Search all (URL / method / type / status code…)"}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Select value={method} onValueChange={setMethod}>
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={"Method"} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{"All methods"}</SelectItem>
            {METHODS.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={String(size)} onValueChange={(v) => setSize(Number(v))}>
          <SelectTrigger size="sm" className="w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n} {"/ page"}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          <span className="tabular-nums">
            {rangeStart}–{rangeEnd} / {total}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page <= 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeftIcon />
          </Button>
          <span className="tabular-nums">
            {page + 1} / {pageCount}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page + 1 >= pageCount}
            onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground">{"Selected"} {selectedFlows.size} {" traffic records"}</span>
        <Button variant="outline" size="sm" disabled={selectedFlows.size === 0} onClick={() => setLinking(true)}>
          {"Link to vulnerability"}
        </Button>
        {selectedFlows.size > 0 ? (
          <Button variant="ghost" size="sm" onClick={() => setSelectedFlows(new Set())}>
            {"Clear selection"}
          </Button>
        ) : null}
      </div>
      {/* History table */}
      <div className="flex h-[calc(100vh-15rem)] min-h-0 flex-col">
        <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
          <div className="min-h-0 flex-1 overflow-auto">
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-10">
                    <Checkbox
                      aria-label={"Select traffic on this page"}
                      checked={exchanges.length > 0 && exchanges.every((e) => selectedFlows.has(e.id))}
                      onCheckedChange={(checked) =>
                        setSelectedFlows((previous) => {
                          const next = new Set(previous);
                          for (const e of exchanges) {
                            if (checked === true) next.add(e.id);
                            else next.delete(e.id);
                          }
                          return next;
                        })
                      }
                    />
                  </TableHead>
                  <TableHead className="w-36">{"Time"}</TableHead>
                  <TableHead className="w-44">host</TableHead>
                  <TableHead className="w-20">{"Method"}</TableHead>
                  <TableHead>URL</TableHead>
                  <TableHead className="w-20">{"Status code"}</TableHead>
                  <TableHead className="w-36">content-type</TableHead>
                  <TableHead className="w-24 text-right">{"Response length"}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {exchanges.map((e) => (
                  <TableRow
                    key={e.id}
                    className={cn("cursor-pointer", selected?.id === e.id && "bg-accent hover:bg-accent")}
                    onClick={() => setSelected(e)}
                  >
                    <TableCell>
                      <Checkbox
                        aria-label={`Select traffic ${e.id}`}
                        checked={selectedFlows.has(e.id)}
                        onClick={(event) => event.stopPropagation()}
                        onCheckedChange={(checked) =>
                          setSelectedFlows((previous) => {
                            const next = new Set(previous);
                            if (checked === true) next.add(e.id);
                            else next.delete(e.id);
                            return next;
                          })
                        }
                      />
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(e.ts)}</TableCell>
                    <TableCell className="font-mono text-xs">{e.host}</TableCell>
                    <TableCell>
                      <MethodBadge method={e.method} />
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span className="block truncate font-mono text-xs">{e.url}</span>
                    </TableCell>
                    <TableCell>
                      <span className={cn("font-mono text-xs font-semibold tabular-nums", statusTone(e.status))}>
                        {e.status}
                      </span>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{e.content_type}</TableCell>
                    <TableCell className="text-right text-xs tabular-nums">{fmtBytes(e.resp_len)}</TableCell>
                  </TableRow>
                ))}
                {exchanges.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={8} className="py-12 text-center text-sm text-muted-foreground">
                      {traffic === null ? "Loading…" : "No matching traffic."}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </Card>
      </div>

      {linking ? (
        <LinkTrafficDialog
          trafficIds={[...selectedFlows]}
          onClose={() => setLinking(false)}
          onBound={() => setSelectedFlows(new Set())}
        />
      ) : null}
      <Sheet open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <SheetContent className="w-full! max-w-none! gap-0 p-0 sm:w-[48rem]! sm:max-w-[48rem]!">
          {selected && (
            <>
              <SheetHeader className="border-b px-5 py-4">
                <div className="flex items-center gap-2 pr-8">
                  <MethodBadge method={selected.method} />
                  <Badge variant="secondary" className={cn("font-mono tabular-nums", statusTone(selected.status))}>
                    {selected.status}
                  </Badge>
                  <span className="ml-auto text-xs text-muted-foreground tabular-nums">{fmtTime(selected.ts)}</span>
                </div>
                <SheetTitle className="break-all font-mono">{selected.host}</SheetTitle>
                <SheetDescription className="break-all font-mono">{selected.url}</SheetDescription>
              </SheetHeader>
              <Tabs defaultValue="request" className="min-h-0 flex-1 gap-0">
                <TabsList className="mx-5 mt-4 grid w-auto grid-cols-2">
                  <TabsTrigger value="request">{"Request"}</TabsTrigger>
                  <TabsTrigger value="response">{"Response"}</TabsTrigger>
                </TabsList>
                <TabsContent value="request" className="min-h-0 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-5 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      {"Loading message…"}
                    </div>
                  ) : (
                    <HttpCodeBlock raw={requestWithHost(detail?.req ?? "", selected)} />
                  )}
                </TabsContent>
                <TabsContent value="response" className="min-h-0 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-5 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      {"Loading message…"}
                    </div>
                  ) : (
                    <HttpCodeBlock raw={detail?.resp ?? ""} />
                  )}
                </TabsContent>
              </Tabs>
            </>
          )}
        </SheetContent>
      </Sheet>

      <AlertDialog
        open={deleteMode !== null}
        onOpenChange={(o) => {
          if (!o) setDeleteMode(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {deleteMode === "selected"
                ? `Delete selected ${selectedHosts.length} targets' entire traffic?`
                : "Delete all traffic for this target?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {deleteMode === "selected" ? (
                <>
                  {"Permanently delete"} <span className="font-semibold tabular-nums">{selectedHosts.length}</span> {" targets ("}
                  <span className="font-mono">
                    {selectedHosts.slice(0, 3).join("、")}
                    {selectedHosts.length > 3 ? "…" : ""}
                  </span>
                  {")'s entire traffic records, including raw requests and responses. This action cannot be undone."}
                </>
              ) : (
                <>
                  {"Permanently delete traffic whose host contains"} <span className="font-mono font-semibold">{hostQ}</span>{" "}
                  {" and all its traffic records (including raw requests/responses). This action cannot be undone."}
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>{"Cancel"}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "Deleting…" : "Confirm deletion"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
