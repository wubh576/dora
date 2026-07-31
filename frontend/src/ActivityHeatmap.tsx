import { useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import type { TimelinePoint } from "./api";
import { formatNumber, formatTokenCompact } from "./format";

const DISPLAY_WEEKS = 53;
const DAYS_PER_WEEK = 7;

type ActivityHeatmapProps = {
  startDate: string;
  endDate: string;
  days: TimelinePoint[];
};

type HeatmapDay = {
  date: string;
  totalTokens: number;
  state: "before" | "tracked" | "future";
  level: number;
};

export function ActivityHeatmap({ startDate, endDate, days }: ActivityHeatmapProps) {
  const scrollContainer = useRef<HTMLDivElement>(null);
  const [activeDescription, setActiveDescription] = useState("");
  const calendar = buildCalendar(startDate, endDate, days);
  const weekCount = calendar.length / DAYS_PER_WEEK;
  const trackedDays = days.filter((day) => day.totalTokens > 0);
  const totalTokens = trackedDays.reduce((total, day) => total + day.totalTokens, 0);
  const longestStreak = calculateLongestStreak(startDate, endDate, trackedDays);
  const monthLabels = buildMonthLabels(calendar);
  const gridStyle = { "--heatmap-weeks": weekCount } as CSSProperties;

  useLayoutEffect(() => {
    const container = scrollContainer.current;
    if (container) {
      container.scrollLeft = container.scrollWidth;
    }
  }, [startDate, endDate]);

  return (
    <section className="panel activity-panel">
      <div className="activity-heading">
        <div>
          <p className="panel-label">Token 热力图</p>
          <h2>每一天的编码足迹</h2>
          <p>统计始于 {formatChineseDate(startDate)}，颜色越深代表当天使用的 token 越多。</p>
        </div>
        <div className="activity-stats" aria-label="热力图统计">
          <div>
            <span>累计 Token</span>
            <strong
              title={`${formatNumber(totalTokens)} token`}
              aria-label={`${formatNumber(totalTokens)} token`}
            >
              {formatTokenCompact(totalTokens)}
            </strong>
          </div>
          <div>
            <span>活跃天数</span>
            <strong>{trackedDays.length}</strong>
          </div>
          <div>
            <span>最长连续</span>
            <strong>{longestStreak} 天</strong>
          </div>
        </div>
      </div>

      <div className="heatmap-scroll" ref={scrollContainer}>
        <div className="heatmap-calendar" style={gridStyle}>
          <div className="heatmap-months" aria-hidden="true">
            {monthLabels.map((month) => (
              <span key={`${month.label}-${month.column}`} style={{ gridColumnStart: month.column }}>
                {month.label}
              </span>
            ))}
          </div>
          <div className="heatmap-body">
            <div className="heatmap-weekdays" aria-hidden="true">
              <span>一</span>
              <span>三</span>
              <span>五</span>
            </div>
            <div
              className="heatmap-grid"
              role="group"
              aria-label={`Dora Token 活跃热力图，统计始于 ${formatChineseDate(startDate)}`}
            >
              {calendar.map((day) => {
                const isActive = day.state === "tracked" && day.totalTokens > 0;
                const description =
                  day.state === "before"
                    ? `${formatChineseDate(day.date)}，尚未开始统计`
                    : day.state === "future"
                      ? `${formatChineseDate(day.date)}，未来日期`
                      : `${formatChineseDate(day.date)}，${formatNumber(day.totalTokens)} token`;
                return (
                  <time
                    className={`heatmap-cell ${day.state}`}
                    data-level={day.level}
                    dateTime={day.date}
                    key={day.date}
                    title={description}
                    aria-hidden={!isActive}
                    aria-label={isActive ? description : undefined}
                    tabIndex={isActive ? 0 : undefined}
                    onBlur={() => setActiveDescription("")}
                    onFocus={() => setActiveDescription(description)}
                    onMouseEnter={() => isActive && setActiveDescription(description)}
                    onMouseLeave={() => setActiveDescription("")}
                  />
                );
              })}
            </div>
          </div>
        </div>
      </div>

      <div className="heatmap-footer">
        <span className="heatmap-detail" aria-live="polite">
          {activeDescription || "过去 53 周"}
        </span>
        <div className="heatmap-legend" aria-label="Token 强度图例">
          <span>少</span>
          {[0, 1, 2, 3, 4].map((level) => (
            <i data-level={level} key={level} aria-hidden="true" />
          ))}
          <span>多</span>
        </div>
      </div>
    </section>
  );
}

function buildCalendar(startDate: string, endDate: string, days: TimelinePoint[]) {
  const totals = new Map(days.map((day) => [day.date, day.totalTokens]));
  const maximum = Math.max(...days.map((day) => day.totalTokens), 0);
  const end = parseDate(endDate);
  const endOfWeek = addDays(end, DAYS_PER_WEEK - 1 - end.getDay());
  const displayStart = addDays(endOfWeek, -((DISPLAY_WEEKS * DAYS_PER_WEEK) - 1));
  const calendar: HeatmapDay[] = [];

  for (let index = 0; index < DISPLAY_WEEKS * DAYS_PER_WEEK; index += 1) {
    const date = addDays(displayStart, index);
    const key = dateKey(date);
    const totalTokens = totals.get(key) ?? 0;
    const state = key < startDate ? "before" : key > endDate ? "future" : "tracked";
    calendar.push({
      date: key,
      totalTokens,
      state,
      level: state === "tracked" ? heatLevel(totalTokens, maximum) : 0,
    });
  }
  return calendar;
}

function buildMonthLabels(calendar: HeatmapDay[]) {
  const labels: Array<{ label: string; column: number }> = [];
  let previousMonth = "";
  for (let column = 0; column < calendar.length / DAYS_PER_WEEK; column += 1) {
    const representative = parseDate(calendar[column * DAYS_PER_WEEK + 3].date);
    const month = `${representative.getFullYear()}-${representative.getMonth()}`;
    if (month !== previousMonth) {
      labels.push({
        label: `${representative.getMonth() + 1}月`,
        column: column + 1,
      });
      previousMonth = month;
    }
  }
  return labels;
}

function calculateLongestStreak(startDate: string, endDate: string, days: TimelinePoint[]) {
  const activeDates = new Set(days.filter((day) => day.totalTokens > 0).map((day) => day.date));
  const endTime = parseDate(endDate).getTime();
  let current = 0;
  let longest = 0;
  for (
    let date = parseDate(startDate);
    date.getTime() <= endTime;
    date = addDays(date, 1)
  ) {
    if (activeDates.has(dateKey(date))) {
      current += 1;
      longest = Math.max(longest, current);
    } else {
      current = 0;
    }
  }
  return longest;
}

function heatLevel(value: number, maximum: number) {
  if (value <= 0 || maximum <= 0) {
    return 0;
  }
  return Math.max(1, Math.ceil(Math.sqrt(value / maximum) * 4));
}

function parseDate(value: string) {
  const [year, month, day] = value.split("-").map(Number);
  return new Date(year, month - 1, day);
}

function addDays(value: Date, days: number) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate() + days);
}

function dateKey(value: Date) {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function formatChineseDate(value: string) {
  const date = parseDate(value);
  return `${date.getFullYear()} 年 ${date.getMonth() + 1} 月 ${date.getDate()} 日`;
}
