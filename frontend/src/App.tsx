import { useEffect, useState } from "react";
import { ActivityHeatmap } from "./ActivityHeatmap";
import { formatNumber, formatTokenCompact } from "./format";
import {
  type BreakdownItem,
  type DashboardData,
  type DoraSettings,
  type HealthStatus,
  type QuotaData,
  type QuotaItem,
  type TimelinePoint,
  type UsageRange,
  loadDashboard,
  loadHealth,
  loadQuotas,
  loadSettings,
  refreshCodexQuota,
  scanUsage,
  updateSettings,
} from "./api";

type LoadState<T> =
  | { kind: "loading" }
  | { kind: "ready"; value: T }
  | { kind: "error"; message: string };

const ranges: UsageRange[] = ["1D", "7D", "30D", "ALL"];

function App() {
  const [view, setView] = useState<"dashboard" | "diagnostics">("dashboard");
  const [range, setRange] = useState<UsageRange>("7D");
  const [health, setHealth] = useState<LoadState<HealthStatus>>({ kind: "loading" });
  const [dashboard, setDashboard] = useState<LoadState<DashboardData>>({ kind: "loading" });
  const [quota, setQuota] = useState<LoadState<QuotaData>>({ kind: "loading" });
  const [settings, setSettings] = useState<LoadState<DoraSettings>>({ kind: "loading" });
  const [refreshVersion, setRefreshVersion] = useState(0);
  const [refreshing, setRefreshing] = useState<"incremental" | "full" | null>(null);
  const [refreshMessage, setRefreshMessage] = useState("");
  const [quotaBusy, setQuotaBusy] = useState(false);
  const [quotaMessage, setQuotaMessage] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    loadHealth(controller.signal)
      .then((value) => setHealth({ kind: "ready", value }))
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setHealth({ kind: "error", message: errorMessage(error) });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([loadQuotas(controller.signal), loadSettings(controller.signal)])
      .then(([quotaValue, settingsValue]) => {
        setQuota({ kind: "ready", value: quotaValue });
        setSettings({ kind: "ready", value: settingsValue });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          const message = errorMessage(error);
          setQuota({ kind: "error", message });
          setSettings({ kind: "error", message });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setDashboard({ kind: "loading" });
    loadDashboard(range, controller.signal)
      .then((value) => setDashboard({ kind: "ready", value }))
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setDashboard({ kind: "error", message: errorMessage(error) });
        }
      });
    return () => controller.abort();
  }, [range, refreshVersion]);

  async function refresh(full: boolean) {
    if (health.kind !== "ready") {
      return;
    }
    setRefreshing(full ? "full" : "incremental");
    setRefreshMessage("");
    try {
      const result = await scanUsage(health.value.controlToken, full);
      const imported =
        result.mode === "full"
          ? `已校验 ${formatNumber(result.eventsSeen)} 条记录`
          : result.eventsSeen > 0
          ? `解析到 ${formatNumber(result.eventsSeen)} 条新记录`
          : "已是最新状态";
      setRefreshMessage(`${imported} · 共保存 ${formatNumber(result.eventsStored)} 条事件`);
    } catch (error) {
      setRefreshMessage(errorMessage(error));
    } finally {
      setRefreshing(null);
      setRefreshVersion((value) => value + 1);
    }
  }

  async function refreshQuota() {
    if (health.kind !== "ready") {
      return;
    }
    setQuotaBusy(true);
    setQuotaMessage("");
    try {
      const value = await refreshCodexQuota(health.value.controlToken);
      setQuota({ kind: "ready", value });
      setQuotaMessage("订阅配额已刷新");
    } catch (error) {
      setQuotaMessage(errorMessage(error));
      try {
        setQuota({ kind: "ready", value: await loadQuotas() });
      } catch (reloadError) {
        setQuota({ kind: "error", message: errorMessage(reloadError) });
      }
    } finally {
      setQuotaBusy(false);
    }
  }

  async function setQuotaConsent(enabled: boolean) {
    if (health.kind !== "ready") {
      return;
    }
    setQuotaBusy(true);
    setQuotaMessage("");
    try {
      const values = await updateSettings(health.value.controlToken, enabled);
      setSettings({ kind: "ready", value: values });
      if (enabled) {
        const value = await refreshCodexQuota(health.value.controlToken);
        setQuota({ kind: "ready", value });
        setQuotaMessage("已启用 Codex 订阅配额");
      } else {
        setQuota({ kind: "ready", value: await loadQuotas() });
        setQuotaMessage("已关闭 Codex 订阅配额");
      }
    } catch (error) {
      setQuotaMessage(errorMessage(error));
      try {
        const [quotaValue, settingsValue] = await Promise.all([loadQuotas(), loadSettings()]);
        setQuota({ kind: "ready", value: quotaValue });
        setSettings({ kind: "ready", value: settingsValue });
      } catch (reloadError) {
        const message = errorMessage(reloadError);
        setQuota({ kind: "error", message });
        setSettings({ kind: "error", message });
      }
    } finally {
      setQuotaBusy(false);
    }
  }

  const online = health.kind === "ready" && health.value.backend && health.value.sqlite;

  return (
    <div className="app-shell">
      <header className="topbar">
        <button className="brand" type="button" onClick={() => setView("dashboard")}>
          <span className="brand-mark" aria-hidden="true">D</span>
          <span>Dora</span>
        </button>

        <nav className="primary-nav" aria-label="主导航">
          <button
            className={view === "dashboard" ? "active" : ""}
            type="button"
            aria-pressed={view === "dashboard"}
            onClick={() => setView("dashboard")}
          >
            概览
          </button>
          <button
            className={view === "diagnostics" ? "active" : ""}
            type="button"
            aria-pressed={view === "diagnostics"}
            onClick={() => setView("diagnostics")}
          >
            诊断
          </button>
        </nav>

        <div className={`connection-pill ${online ? "online" : ""}`}>
          <span aria-hidden="true" />
          {online ? "本地数据" : "连接断开"}
        </div>
      </header>

      <main className="content">
        {health.kind === "error" && (
          <Notice
            tone="error"
            title="Dora 后端暂时不可用"
            body={`请启动后端后刷新页面。${health.message}`}
          />
        )}

        {view === "dashboard" ? (
          <Dashboard
            range={range}
            setRange={setRange}
            state={dashboard}
            refreshing={refreshing !== null}
            refreshMessage={refreshMessage}
            onRefresh={() => void refresh(false)}
            quota={quota}
            quotaBusy={quotaBusy}
            quotaMessage={quotaMessage}
            onQuotaRefresh={() => void refreshQuota()}
          />
        ) : (
          <Diagnostics
            state={dashboard}
            health={health}
            refreshing={refreshing}
            refreshMessage={refreshMessage}
            onScan={(full) => void refresh(full)}
            quota={quota}
            settings={settings}
            quotaBusy={quotaBusy}
            quotaMessage={quotaMessage}
            onQuotaRefresh={() => void refreshQuota()}
            onQuotaConsent={(enabled) => void setQuotaConsent(enabled)}
          />
        )}
      </main>
    </div>
  );
}

type DashboardProps = {
  range: UsageRange;
  setRange: (range: UsageRange) => void;
  state: LoadState<DashboardData>;
  refreshing: boolean;
  refreshMessage: string;
  onRefresh: () => void;
  quota: LoadState<QuotaData>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
};

function Dashboard({
  range,
  setRange,
  state,
  refreshing,
  refreshMessage,
  onRefresh,
  quota,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
}: DashboardProps) {
  return (
    <>
      <section className="page-heading">
        <div>
          <p className="eyebrow">CODEX 用量</p>
          <h1>让每一次编码，<br />都清清楚楚。</h1>
          <p className="lede">只在这台 Mac 上，安静地整理你的 Codex token 用量。</p>
        </div>
        <button className="refresh-button" type="button" disabled={refreshing} onClick={onRefresh}>
          <RefreshIcon spinning={refreshing} />
          {refreshing ? "正在扫描…" : "刷新用量"}
        </button>
      </section>

      <div className="range-row">
        <div className="range-picker" aria-label="用量时间范围">
          {ranges.map((value) => (
            <button
              className={range === value ? "active" : ""}
              type="button"
              aria-pressed={range === value}
              key={value}
              onClick={() => setRange(value)}
            >
              {value}
            </button>
          ))}
        </div>
        {refreshMessage && <p className="refresh-message" role="status">{refreshMessage}</p>}
      </div>

      {state.kind === "loading" && <DashboardSkeleton />}
      {state.kind === "error" && (
        <Notice tone="error" title="暂时无法读取用量" body={state.message} />
      )}
      {state.kind === "ready" && (
        <DashboardContent
          data={state.value}
          quota={quota}
          quotaBusy={quotaBusy}
          quotaMessage={quotaMessage}
          onQuotaRefresh={onQuotaRefresh}
        />
      )}
    </>
  );
}

function DashboardContent({
  data,
  quota,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
}: {
  data: DashboardData;
  quota: LoadState<QuotaData>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
}) {
  const { summary, diagnostics } = data;
  if (summary.eventCount === 0) {
    return (
      <>
        {(diagnostics.status === "error" || diagnostics.status === "degraded") && (
          <Notice
            tone={diagnostics.status === "error" ? "error" : "warning"}
            title={diagnostics.message}
            body={diagnostics.advice}
          />
        )}
        <section className="empty-state">
          <div className="empty-mark">0</div>
          <p className="eyebrow">暂无用量</p>
          <h2>{diagnostics.filesSeen === 0 ? "还没有找到 Codex 会话" : "这个时间范围内暂无 token 记录"}</h2>
          <p>
            {diagnostics.filesSeen === 0
              ? "先运行一次 Codex，再刷新用量。Dora 会扫描本地 sessions 与 archived_sessions 目录。"
              : "可以切换到更长的时间范围，或创建一次新的 Codex 会话。"}
          </p>
        </section>
        <ActivityHeatmap {...data.activity} />
        <QuotaPanel
          state={quota}
          busy={quotaBusy}
          message={quotaMessage}
          onRefresh={onQuotaRefresh}
        />
      </>
    );
  }

  return (
    <>
      {diagnostics.status === "error" && (
        <Notice tone="warning" title={diagnostics.message} body={diagnostics.advice} />
      )}

      <section className="summary-grid">
        <article className="hero-metric panel">
          <p className="panel-label">Token 总量 · {summary.range}</p>
          <strong>{formatTokenCompact(summary.totalTokens)}</strong>
          <span>{formatNumber(summary.totalTokens)} 个精确 token</span>
          <div className="hero-meta">
            <div>
              <small>事件数</small>
              <b>{formatNumber(summary.eventCount)}</b>
            </div>
            <div>
              <small>Cache 命中</small>
              <b>{formatPercent(summary.cacheHitRate)}</b>
            </div>
          </div>
        </article>

        <article className="token-mix panel">
          <div className="panel-heading">
            <div>
              <p className="panel-label">Token 构成</p>
              <h2>五类互不重叠的用量</h2>
            </div>
          </div>
          <div className="metric-list">
            <Metric label="Input" value={summary.inputTokens} color="input" />
            <Metric label="Cache read" value={summary.cachedInputTokens} color="cache" />
            <Metric label="Cache creation" value={summary.cacheCreationInputTokens} color="creation" />
            <Metric label="Output" value={summary.outputTokens} color="output" />
            <Metric label="Reasoning" value={summary.reasoningOutputTokens} color="reasoning" />
          </div>
        </article>
      </section>

      <ActivityHeatmap {...data.activity} />

      <section className="panel trend-panel">
        <div className="panel-heading">
          <div>
            <p className="panel-label">每日趋势</p>
            <h2>Token 都花在了哪里</h2>
          </div>
          <TokenLegend />
        </div>
        <TimelineChart points={data.timeline} />
      </section>

      <section className="breakdown-grid">
        <BreakdownCard title="模型" eyebrow="模型分布" items={data.models} />
        <BreakdownCard title="项目" eyebrow="项目分布" items={data.projects} />
      </section>

      <QuotaPanel
        state={quota}
        busy={quotaBusy}
        message={quotaMessage}
        onRefresh={onQuotaRefresh}
      />
    </>
  );
}

function Metric({ label, value, color }: { label: string; value: number; color: string }) {
  return (
    <div>
      <span className={`legend-dot ${color}`} aria-hidden="true" />
      <p>{label}</p>
      <strong>{formatTokenCompact(value)}</strong>
      <small>{formatNumber(value)}</small>
    </div>
  );
}

function TimelineChart({ points }: { points: TimelinePoint[] }) {
  const max = Math.max(...points.map((point) => point.totalTokens), 1);
  return (
    <div className="chart-scroll">
      <div className="chart" aria-hidden="true">
        {points.map((point) => {
          const height = Math.max(10, Math.round((point.totalTokens / max) * 190));
          return (
            <div className="chart-column" key={point.date} title={`${point.date}: ${formatNumber(point.totalTokens)}`}>
              <div className="bar-value">{formatTokenCompact(point.totalTokens)}</div>
              <div className="stacked-bar" style={{ height }}>
                <BarSegment value={point.reasoningOutputTokens} total={point.totalTokens} color="reasoning" />
                <BarSegment value={point.outputTokens} total={point.totalTokens} color="output" />
                <BarSegment value={point.cacheCreationInputTokens} total={point.totalTokens} color="creation" />
                <BarSegment value={point.cachedInputTokens} total={point.totalTokens} color="cache" />
                <BarSegment value={point.inputTokens} total={point.totalTokens} color="input" />
              </div>
              <time dateTime={point.date}>{point.date.slice(5)}</time>
            </div>
          );
        })}
      </div>
      <div className="sr-only">
        <table>
          <caption>按 token 类型统计的每日用量</caption>
          <thead>
            <tr>
              <th>日期</th>
              <th>总量</th>
              <th>Input</th>
              <th>Cache read</th>
              <th>Cache creation</th>
              <th>Output</th>
              <th>Reasoning</th>
            </tr>
          </thead>
          <tbody>
            {points.map((point) => (
              <tr key={point.date}>
                <th>{point.date}</th>
                <td>{point.totalTokens}</td>
                <td>{point.inputTokens}</td>
                <td>{point.cachedInputTokens}</td>
                <td>{point.cacheCreationInputTokens}</td>
                <td>{point.outputTokens}</td>
                <td>{point.reasoningOutputTokens}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function BarSegment({ value, total, color }: { value: number; total: number; color: string }) {
  if (value <= 0 || total <= 0) return null;
  return <span className={color} style={{ height: `${(value / total) * 100}%` }} />;
}

function QuotaPanel({
  state,
  busy,
  message,
  onRefresh,
}: {
  state: LoadState<QuotaData>;
  busy: boolean;
  message: string;
  onRefresh: () => void;
}) {
  if (state.kind === "loading") {
    return <section className="panel quota-placeholder" aria-label="正在加载 Codex 配额" />;
  }
  if (state.kind === "error") {
    return (
      <section className="panel quota-placeholder">
        <div>
          <p className="panel-label">Codex 配额</p>
          <h2>暂时无法读取配额</h2>
          <p>{state.message} 本地 token 统计仍可正常使用。</p>
        </div>
        <span className="muted-badge error">不可用</span>
      </section>
    );
  }

  const value = state.value;
  if (!value.enabled) {
    return (
      <section className="panel quota-placeholder">
        <div>
          <p className="panel-label">Codex 配额</p>
          <h2>尚未启用订阅配额</h2>
          <p>在诊断页明确授权后即可读取；本地 token 统计不会受到影响。</p>
        </div>
        <span className="muted-badge">未启用</span>
      </section>
    );
  }

  return (
    <section className="panel quota-panel">
      <div className="panel-heading quota-heading">
        <div>
          <p className="panel-label">Codex 配额</p>
          <h2>订阅配额窗口</h2>
          {value.items[0]?.accountLabel && (
            <p className="quota-account">
              {value.items[0].accountLabel}
              {value.items[0].plan ? ` · ${value.items[0].plan}` : ""}
            </p>
          )}
        </div>
        <button type="button" disabled={busy} onClick={onRefresh}>
          {busy ? "正在刷新…" : "刷新配额"}
        </button>
      </div>

      {value.status !== "ready" && value.items.length > 0 && (
        <Notice tone="warning" title={value.message} body={value.advice} />
      )}
      {value.items.length === 0 ? (
        <div className="quota-empty">
          <strong>{value.message}</strong>
          <p>{value.advice || "Codex OAuth 可用后再刷新。"}</p>
        </div>
      ) : (
        <div className="quota-grid">
          {value.items.map((item) => <QuotaCard key={item.windowKey} item={item} />)}
        </div>
      )}
      {message && <p className="refresh-message quota-message" role="status">{message}</p>}
    </section>
  );
}

function QuotaCard({ item }: { item: QuotaItem }) {
  const label = quotaWindowLabel(item.windowKey);
  return (
    <article className="quota-card">
      <div className="quota-card-heading">
        <div>
          <span>{label}</span>
          {item.sourceState === "stale" && <small>已过期</small>}
        </div>
        <strong>剩余 {formatPercentValue(item.remainingPercent)}</strong>
      </div>
      <div
        className="quota-progress"
        role="progressbar"
        aria-label={`${label}配额已使用`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={item.usedPercent}
      >
        <span style={{ width: `${item.usedPercent}%` }} />
      </div>
      <div className="quota-meta">
        <span>已用 {formatPercentValue(item.usedPercent)}</span>
        <span>重置于 {formatDate(item.resetsAt)}</span>
      </div>
    </article>
  );
}

function TokenLegend() {
  return (
    <div className="token-legend">
      {[
        ["Input", "input"],
        ["Cache", "cache"],
        ["Create", "creation"],
        ["Output", "output"],
        ["Reason", "reasoning"],
      ].map(([label, color]) => (
        <span key={label}><i className={color} />{label}</span>
      ))}
    </div>
  );
}

function BreakdownCard({ title, eyebrow, items }: { title: string; eyebrow: string; items: BreakdownItem[] }) {
  const maximum = Math.max(items[0]?.totalTokens ?? 0, 1);
  return (
    <article className="panel breakdown-card">
      <div className="panel-heading">
        <div>
          <p className="panel-label">{eyebrow}</p>
          <h2>{title}</h2>
        </div>
      </div>
      <div className="ranking-list">
        {items.slice(0, 6).map((item) => (
          <div key={item.name}>
            <div className="ranking-label">
              <span title={item.name}>{item.name}</span>
              <b
                title={`${formatNumber(item.totalTokens)} token`}
                aria-label={`${formatNumber(item.totalTokens)} token`}
              >
                {formatTokenCompact(item.totalTokens)}
              </b>
            </div>
            <div className="ranking-track">
              <span style={{ width: `${Math.max(3, (item.totalTokens / maximum) * 100)}%` }} />
            </div>
          </div>
        ))}
      </div>
    </article>
  );
}

type DiagnosticsProps = {
  state: LoadState<DashboardData>;
  health: LoadState<HealthStatus>;
  refreshing: "incremental" | "full" | null;
  refreshMessage: string;
  onScan: (full: boolean) => void;
  quota: LoadState<QuotaData>;
  settings: LoadState<DoraSettings>;
  quotaBusy: boolean;
  quotaMessage: string;
  onQuotaRefresh: () => void;
  onQuotaConsent: (enabled: boolean) => void;
};

function Diagnostics({
  state,
  health,
  refreshing,
  refreshMessage,
  onScan,
  quota,
  settings,
  quotaBusy,
  quotaMessage,
  onQuotaRefresh,
  onQuotaConsent,
}: DiagnosticsProps) {
  const data = state.kind === "ready" ? state.value.diagnostics : null;
  const quotaData = quota.kind === "ready" ? quota.value : null;
  const quotaConsent = settings.kind === "ready" && settings.value.codexQuotaConsent;
  return (
    <>
      <section className="page-heading compact">
        <div>
          <p className="eyebrow">诊断</p>
          <h1>本地数据，一切有据可查。</h1>
          <p className="lede">用最少的信息，说明发现、解析与持久化是否正常。</p>
        </div>
      </section>

      {state.kind === "loading" && <DashboardSkeleton />}
      {state.kind === "error" && <Notice tone="error" title="诊断信息暂时不可用" body={state.message} />}
      {data && (
        <>
          <section className="diagnostic-grid">
            <DiagnosticMetric label="状态" value={humanStatus(data.status)} tone={data.status} />
            <DiagnosticMetric label="扫描文件" value={formatNumber(data.filesSeen)} />
            <DiagnosticMetric label="已存事件" value={formatNumber(data.storedEvents)} />
            <DiagnosticMetric label="Parser 版本" value={`v${data.parserVersion}`} />
          </section>

          <section className="panel diagnostic-detail">
            <div>
              <p className="panel-label">Codex 数据源</p>
              <h2>自动发现本地数据</h2>
            </div>
            <dl>
              <div><dt>扫描目录</dt><dd>sessions + archived_sessions</dd></div>
              <div><dt>最近扫描</dt><dd>{formatDate(data.lastScanAt)}</dd></div>
              <div><dt>扫描模式</dt><dd>{humanScanMode(data.lastRunMode)}</dd></div>
              <div><dt>最近解析</dt><dd>{formatNumber(data.eventsSeen)} 条记录</dd></div>
              <div>
                <dt>Dora 初始化</dt>
                <dd>{health.kind === "ready" ? formatDate(health.value.initializedAt) : "不可用"}</dd>
              </div>
            </dl>
            <div className="diagnostic-actions">
              <button type="button" disabled={refreshing !== null} onClick={() => onScan(false)}>
                {refreshing === "incremental" ? "正在扫描…" : "扫描变化"}
              </button>
              <button className="secondary" type="button" disabled={refreshing !== null} onClick={() => onScan(true)}>
                {refreshing === "full" ? "正在重建…" : "全量重建"}
              </button>
            </div>
            {refreshMessage && <p className="refresh-message" role="status">{refreshMessage}</p>}
          </section>

          <Notice
            tone="neutral"
            title="你的对话始终保持私密"
            body="Dora 只保存 token 元数据与扫描 checkpoint，不会把 prompt、回复、工具参数或原始会话内容写入 SQLite。"
          />
        </>
      )}

      <section className="panel quota-settings">
        <div className="quota-settings-copy">
          <p className="panel-label">Codex 订阅配额</p>
          <h2>允许 Dora 读取配额</h2>
          <p>
            开启后，Dora 会读取本地 Codex OAuth 会话，并且只访问 ChatGPT 配额接口。
            Access token 不会写入 SQLite、设置或日志。
          </p>
        </div>
        <label className="consent-toggle">
          <input
            type="checkbox"
            checked={quotaConsent}
            disabled={settings.kind !== "ready" || quotaBusy || health.kind !== "ready"}
            onChange={(event) => onQuotaConsent(event.target.checked)}
          />
          <span aria-hidden="true" />
          <b>{quotaConsent ? "已启用" : "未启用"}</b>
        </label>

        {settings.kind === "error" && (
          <Notice tone="error" title="设置暂时不可用" body={settings.message} />
        )}
        {quota.kind === "error" && (
          <Notice tone="error" title="配额状态暂时不可用" body={quota.message} />
        )}
        {quotaData && quotaData.enabled && (
          <div className="quota-setting-status">
            <div>
              <span>状态</span>
              <strong>{humanQuotaStatus(quotaData.status)}</strong>
            </div>
            <div>
              <span>最近成功</span>
              <strong>{formatDate(quotaData.lastSuccessAt)}</strong>
            </div>
            <button type="button" disabled={quotaBusy} onClick={onQuotaRefresh}>
              {quotaBusy ? "正在刷新…" : "立即刷新"}
            </button>
          </div>
        )}
        {quotaData && quotaData.status !== "ready" && (
          <p className="quota-advice">{quotaData.message}{quotaData.advice ? ` · ${quotaData.advice}` : ""}</p>
        )}
        {quotaMessage && <p className="refresh-message" role="status">{quotaMessage}</p>}
      </section>
    </>
  );
}

function DiagnosticMetric({ label, value, tone = "" }: { label: string; value: string; tone?: string }) {
  return (
    <article className="panel diagnostic-metric">
      <p className="panel-label">{label}</p>
      <strong className={tone}>{value}</strong>
    </article>
  );
}

function Notice({ tone, title, body }: { tone: "error" | "warning" | "neutral"; title: string; body: string }) {
  return (
    <aside className={`notice ${tone}`} role={tone === "error" ? "alert" : "status"}>
      <span aria-hidden="true">i</span>
      <div><strong>{title}</strong><p>{body}</p></div>
    </aside>
  );
}

function DashboardSkeleton() {
  return (
    <div className="skeleton-grid" aria-label="正在加载用量">
      <div className="skeleton tall" />
      <div className="skeleton tall" />
      <div className="skeleton wide" />
    </div>
  );
}

function RefreshIcon({ spinning }: { spinning: boolean }) {
  return <span className={`refresh-icon ${spinning ? "spinning" : ""}`} aria-hidden="true">↻</span>;
}

function humanStatus(status: string) {
  const labels: Record<string, string> = {
    ready: "正常",
    degraded: "正常，有提示",
    error: "需要处理",
    not_scanned: "尚未扫描",
  };
  return labels[status] ?? status;
}

function humanQuotaStatus(status: string) {
  const labels: Record<string, string> = {
    ready: "正常",
    error: "最近刷新失败",
    unsupported: "不支持当前登录",
    not_configured: "需要登录",
  };
  return labels[status] ?? status;
}

function formatPercent(value: number) {
  return new Intl.NumberFormat(undefined, {
    style: "percent",
    maximumFractionDigits: 1,
  }).format(value);
}

function formatPercentValue(value: number) {
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 1,
  }).format(value) + "%";
}

function formatDate(value: string | null) {
  if (!value) return "尚未";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN");
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "未知错误";
}

function humanScanMode(mode: string) {
  const labels: Record<string, string> = {
    full: "全量",
    incremental: "增量",
    planning: "规划中",
  };
  return mode ? labels[mode] ?? mode : "尚未扫描";
}

function quotaWindowLabel(windowKey: QuotaItem["windowKey"]) {
  return windowKey === "five_hour" ? "5 小时" : "7 天";
}

export default App;
