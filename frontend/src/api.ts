export type HealthStatus = {
  backend: boolean;
  sqlite: boolean;
  initializedAt: string;
  controlToken: string;
};

export type UsageSummary = {
  range: UsageRange;
  startUtc: string;
  endUtc: string;
  cost: CostEstimate;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheCreationInputTokens: number;
  reasoningOutputTokens: number;
  reportedTotalTokens: number;
  cacheHitRate: number;
  eventCount: number;
};

export type CostEstimate = {
  currency: "USD";
  estimatedUsd: number;
  pricedTokens: number;
  unpricedTokens: number;
  coverage: number;
  breakdown: {
    inputUsd: number;
    cacheReadUsd: number;
    cacheCreationUsd: number;
    outputUsd: number;
    reasoningUsd: number;
  };
  checkedAt: string;
  sourceUrl: string;
  basis: string;
  unpricedModels: string[];
};

export type TimelinePoint = {
  date: string;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cacheCreationInputTokens: number;
  reasoningOutputTokens: number;
};

export type BreakdownItem = {
  name: string;
  totalTokens: number;
  eventCount: number;
};

export type UsageDiagnostics = {
  source: string;
  status: string;
  lastScanAt: string | null;
  lastRunMode: string;
  filesSeen: number;
  eventsSeen: number;
  storedEvents: number;
  parserVersion: number;
  message: string;
  advice: string;
};

export type DashboardData = {
  summary: UsageSummary;
  timeline: TimelinePoint[];
  models: BreakdownItem[];
  projects: BreakdownItem[];
  activity: {
    startDate: string;
    endDate: string;
    days: TimelinePoint[];
  };
  diagnostics: UsageDiagnostics;
};

export type ScanResult = {
  mode: string;
  filesSeen: number;
  eventsSeen: number;
  eventsStored: number;
  warnings: string[];
  finishedAt: string;
};

export type QuotaItem = {
  provider: string;
  windowKey: "five_hour" | "seven_day";
  label: string;
  usedPercent: number;
  remainingPercent: number;
  resetsAt: string | null;
  fetchedAt: string;
  sourceState: "confirmed" | "stale";
  plan: string;
  accountLabel: string;
};

export type QuotaData = {
  enabled: boolean;
  status: string;
  lastSuccessAt: string | null;
  items: QuotaItem[];
  message: string;
  advice: string;
};

export type DoraSettings = {
  codexQuotaConsent: boolean;
};

export type UsageRange = "1D" | "7D" | "30D" | "ALL";

const API_BASE_PATH = "/api/v1";

export async function loadHealth(signal?: AbortSignal) {
  return request<HealthStatus>("/health", { signal });
}

export async function loadDashboard(range: UsageRange, signal?: AbortSignal): Promise<DashboardData> {
  const query = encodeURIComponent(range);
  return request<DashboardData>(`/dashboard?range=${query}`, { signal });
}

export async function scanUsage(controlToken: string, full: boolean) {
  return request<ScanResult>(`/scan?full=${full}`, {
    method: "POST",
    headers: {
      "X-Dora-Control-Token": controlToken,
    },
  });
}

export async function loadQuotas(signal?: AbortSignal) {
  return request<QuotaData>("/quotas", { signal });
}

export async function loadSettings(signal?: AbortSignal) {
  return request<DoraSettings>("/settings", { signal });
}

export async function updateSettings(controlToken: string, codexQuotaConsent: boolean) {
  return request<DoraSettings>("/settings", {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-Dora-Control-Token": controlToken,
    },
    body: JSON.stringify({ codexQuotaConsent }),
  });
}

export async function refreshCodexQuota(controlToken: string) {
  return request<QuotaData>("/quota/refresh", {
    method: "POST",
    headers: {
      "X-Dora-Control-Token": controlToken,
    },
  });
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE_PATH}${path}`, {
    ...init,
    cache: "no-store",
  });
  if (!response.ok) {
    const error = await response.json().catch(() => null);
    const advice =
      error && typeof error.advice === "string" ? ` ${error.advice}` : "";
    throw new Error(`请求失败（${response.status}）。${advice}`);
  }
  return (await response.json()) as T;
}
