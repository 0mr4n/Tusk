import { useMemo, useState } from "react"
import { Link as RouterLink, useSearchParams } from "react-router"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { ArrowLeft, Copy, ExternalLink } from "lucide-react"

import { Button, buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Skeleton } from "@/components/ui/skeleton"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"
import { MODE_INFO } from "@/lib/routing"
import { useLinkStats, useRouteStats, type Period, type PeriodStats, type RouteStats, type StatItem } from "@/lib/api"
import { copyText, formatNumber, parseDate, stripProtocol } from "@/lib/format"

const PERIODS: { key: Period; label: string; long: string }[] = [
  { key: "lastDay", label: "Day", long: "last day" },
  { key: "lastWeek", label: "Week", long: "last week" },
  { key: "lastMonth", label: "Month", long: "last month" },
  { key: "lastYear", label: "Year", long: "last year" },
]

/** Axis labels, same as Kutt static/scripts/stats.js createViewsChartLabel. */
function viewLabels(period: Period, now = new Date()): string[] {
  const labels: string[] = []
  const month = (d: Date) => d.toLocaleString("en-US", { month: "short" })
  if (period === "lastDay") {
    const h0 = now.getHours()
    for (let i = 23; i >= 0; --i) labels.push(`${(h0 - i + 24) % 24}:00`)
  } else if (period === "lastWeek" || period === "lastMonth") {
    const n = period === "lastWeek" ? 7 : 30
    for (let i = n - 1; i >= 0; --i) {
      const d = new Date(now)
      d.setDate(now.getDate() - i)
      labels.push(`${d.getDate()} ${month(d)}`)
    }
  } else {
    for (let i = 11; i >= 0; --i) {
      const d = new Date(now)
      d.setDate(1)
      d.setMonth(now.getMonth() - i)
      labels.push(`${month(d)} ${d.getFullYear()}`)
    }
  }
  return labels
}

const BROWSERS: Record<string, string> = { firefox: "Firefox", chrome: "Chrome", edge: "Edge", opera: "Opera", safari: "Safari", other: "Other", ie: "IE" }
const OSES: Record<string, string> = { android: "Android", ios: "iOS", linux: "Linux", macos: "macOS", windows: "Windows", other: "Other" }
const ROUTES: Record<string, string> = { ios: "iOS rule", android: "Android rule", desktop: "Desktop rule", fallback: "Fallback (target)" }
const PLATFORM_OSES: Record<string, string> = { ...OSES, ipados: "iPadOS", chromeos: "ChromeOS" }
const DEVICES: Record<string, string> = { mobile: "Mobile", tablet: "Tablet", desktop: "Desktop", other: "Other" }

/** Platform routing breakdown (TUSK EXTENSION) for one period. */
function RoutingPanel({ stats, period, long }: { stats: RouteStats; period: Period; long: string }) {
  const s = stats[period]
  const nonZero = (items: StatItem[]) => items.filter((i) => i.value > 0)
  return (
    <section aria-labelledby={`routing-${period}`} className="grid gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={`routing-${period}`} className="text-lg font-semibold">
          Platform routing
          {stats.routing && (
            <span className="ml-2 text-sm font-normal text-muted-foreground">
              {MODE_INFO[stats.routing.mode].label}
              {stats.routing.enabled ? "" : " (off)"}
            </span>
          )}
        </h2>
        <p className="text-sm text-muted-foreground">
          {formatNumber(s.total)} routed views in the {long}
        </p>
      </div>
      <div className="grid gap-6 md:grid-cols-3">
        <BarList title="Destinations" items={nonZero(s.stats.route)} format={(n) => ROUTES[n] ?? n} />
        <BarList title="Platforms" items={nonZero(s.stats.os)} format={(n) => PLATFORM_OSES[n] ?? n} />
        <BarList title="Devices" items={nonZero(s.stats.device)} format={(n) => DEVICES[n] ?? n} />
      </div>
      <p className="text-xs text-muted-foreground">
        Counts human clicks that went through routing. iPads in desktop mode report as macOS.
      </p>
    </section>
  )
}

let regionNames: Intl.DisplayNames | null = null
function countryName(code: string): string {
  if (!code || code.toLowerCase() === "unknown") return "Unknown"
  try {
    regionNames ??= new Intl.DisplayNames(["en"], { type: "region" })
    return regionNames.of(code.toUpperCase()) ?? code
  } catch {
    return code
  }
}

function flag(code: string): string {
  if (!/^[a-z]{2}$/i.test(code)) return ""
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => 0x1f1a5 + c.charCodeAt(0)))
}

const chartConfig = { views: { label: "Views", color: "var(--chart-1)" } } satisfies ChartConfig

function ViewsChart({ period, stats }: { period: Period; stats: PeriodStats }) {
  const data = useMemo(() => {
    const labels = viewLabels(period)
    return stats.views.map((v, i) => ({ label: labels[i] ?? String(i), views: v }))
  }, [period, stats.views])
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-64 w-full">
      <AreaChart data={data} margin={{ left: 0, right: 8, top: 8 }}>
        <defs>
          <linearGradient id={`fill-${period}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor="var(--color-views)" stopOpacity={0.6} />
            <stop offset="95%" stopColor="var(--color-views)" stopOpacity={0.05} />
          </linearGradient>
        </defs>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="label" tickLine={false} axisLine={false} tickMargin={8} minTickGap={24} />
        <YAxis allowDecimals={false} tickLine={false} axisLine={false} width={36} />
        <ChartTooltip cursor={false} content={<ChartTooltipContent indicator="line" />} />
        <Area dataKey="views" type="monotone" fill={`url(#fill-${period})`} stroke="var(--color-views)" strokeWidth={2} />
      </AreaChart>
    </ChartContainer>
  )
}

/** Horizontal bar list; used for browsers, OS, referrers and countries. */
function BarList({ title, items, format, limit = 10 }: { title: string; items: StatItem[]; format?: (name: string) => string; limit?: number }) {
  const [all, setAll] = useState(false)
  const total = items.reduce((s, i) => s + i.value, 0)
  const max = items[0]?.value ?? 0
  const shown = all ? items : items.slice(0, limit)
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h3 className="text-base">{title}</h3>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">No data.</p>
        ) : (
          <ul className="grid gap-1.5" aria-label={title}>
            {shown.map((i) => {
              const pct = total ? Math.round((i.value / total) * 100) : 0
              return (
                <li key={i.name} className="relative flex items-center justify-between gap-3 overflow-hidden rounded-md px-2 py-1 text-sm">
                  <span
                    aria-hidden
                    className="absolute inset-y-0 left-0 rounded-md bg-primary/10"
                    style={{ width: `${max ? (i.value / max) * 100 : 0}%` }}
                  />
                  <span className="relative truncate">{format ? format(i.name) : i.name}</span>
                  <span className="relative shrink-0 tabular-nums text-muted-foreground">
                    {formatNumber(i.value)} <span className="text-xs">({pct}%)</span>
                  </span>
                </li>
              )
            })}
          </ul>
        )}
        {items.length > limit && (
          <Button variant="link" size="sm" className="mt-2 px-0" onClick={() => setAll((a) => !a)}>
            {all ? "Show less" : `Show all ${items.length}`}
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

export default function StatsPage() {
  const [params] = useSearchParams()
  const id = params.get("id") ?? ""
  const { data, isPending, error } = useLinkStats(id)
  const [period, setPeriod] = useState<Period>("lastDay")
  const { data: routeStats } = useRouteStats(id, !!data?.routing)

  if (!id) {
    return (
      <Alert variant="destructive">
        <AlertTitle>No link selected</AlertTitle>
        <AlertDescription>Open stats from the links table.</AlertDescription>
      </Alert>
    )
  }
  if (isPending) {
    return (
      <div className="grid gap-4">
        <Skeleton className="h-10 w-2/3" />
        <Skeleton className="h-72 w-full" />
      </div>
    )
  }
  if (error || !data) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Could not load stats</AlertTitle>
        <AlertDescription>{error?.message ?? "Link could not be found."}</AlertDescription>
      </Alert>
    )
  }

  const updated = data.updatedAt ? parseDate(data.updatedAt) : null

  return (
    <div className="grid gap-6">
      <RouterLink to="/" className={buttonVariants({ variant: "ghost", size: "sm", className: "w-fit" })}>
        <ArrowLeft /> Back to links
      </RouterLink>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight break-all sm:text-3xl">
            Stats for{" "}
            <a href={data.link} target="_blank" rel="noopener noreferrer" className="underline-offset-4 hover:underline">
              {stripProtocol(data.link)}
            </a>
          </h1>
          <p className="truncate text-sm text-muted-foreground">
            <ExternalLink className="mr-1 inline size-3.5" />
            {data.target}
          </p>
        </div>
        <div className="flex items-center gap-3 text-sm text-muted-foreground">
          <span>
            Total views: <b className="text-foreground tabular-nums">{formatNumber(data.visit_count)}</b>
          </span>
          <Button variant="outline" size="sm" onClick={() => void copyText(data.link)}>
            <Copy /> Copy
          </Button>
        </div>
      </div>

      <Tabs value={period} onValueChange={(v) => setPeriod(v as Period)}>
        <TabsList aria-label="Period">
          {PERIODS.map((p) => (
            <TabsTrigger key={p.key} value={p.key} className="px-3">
              {p.label}
            </TabsTrigger>
          ))}
        </TabsList>
        {PERIODS.map((p) => {
          const s = data[p.key]
          return (
            <TabsContent key={p.key} value={p.key} className="grid gap-6">
              <Card>
                <CardHeader>
                  <CardTitle>
                    <h2 className="text-lg">
                      {formatNumber(s.total)} views in the {p.long}
                    </h2>
                  </CardTitle>
                  {updated && <CardDescription>Last update {updated.toLocaleString()}</CardDescription>}
                </CardHeader>
                <CardContent>
                  <ViewsChart period={p.key} stats={s} />
                </CardContent>
              </Card>
              <div className="grid gap-6 md:grid-cols-2">
                <BarList title="Referrals" items={s.stats.referrer} format={(n) => n.replace(/\[dot\]/g, ".")} />
                <BarList title="Browsers" items={s.stats.browser} format={(n) => BROWSERS[n] ?? n} />
                <BarList title="Countries" items={s.stats.country} format={(n) => `${flag(n)} ${countryName(n)}`.trim()} />
                <BarList title="Operating systems" items={s.stats.os} format={(n) => OSES[n] ?? n} />
              </div>
              {routeStats && <RoutingPanel stats={routeStats} period={p.key} long={p.long} />}
            </TabsContent>
          )
        })}
      </Tabs>
    </div>
  )
}
