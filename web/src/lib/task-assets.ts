import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "Application",
  endpoint: "API",
  ip: "IP",
  root_domain: "Root domain",
  service: "Service",
  subdomain: "Subdomain",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Discovered by Agent",
  anchor: "Blackboard anchor",
  api: "Asset API",
  company: "Organization link",
  legacy: "Historical link",
  manual: "Manually added",
  system: "System link",
  task: "Task initialization",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
