import { Smartphone } from "lucide-react"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { ROUTING_MODES, ROUTING_PLATFORMS, type Routing, type RoutingMode, type RoutingPlatform } from "@/lib/api"
import { MODE_INFO, PLATFORM_INFO, type RoutingDraft, type RuleDraft } from "@/lib/routing"

/**
 * Platform routing editor (TUSK EXTENSION, docs/UNIVERSAL-LINKS.md).
 * Controlled: the parent owns a RoutingDraft (lib/routing) and turns it into an
 * API payload with draftToInput().
 */

export function RoutingEditor({
  value,
  onChange,
  allowedModes,
  idPrefix,
  fallbackHint,
}: {
  value: RoutingDraft
  onChange: (d: RoutingDraft) => void
  allowedModes?: RoutingMode[]
  idPrefix: string
  fallbackHint?: string
}) {
  const setRule = (p: RoutingPlatform, patch: Partial<RuleDraft>) =>
    onChange({ ...value, rules: { ...value.rules, [p]: { ...value.rules[p], ...patch } } })
  const showApp = value.mode !== "redirect"

  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-3">
        <Label htmlFor={`${idPrefix}-enabled`} className="font-medium">
          Route by platform
        </Label>
        <Switch id={`${idPrefix}-enabled`} checked={value.enabled} onCheckedChange={(v) => onChange({ ...value, enabled: v })} />
      </div>

      <fieldset className={cn("grid gap-4", !value.enabled && "opacity-60")}>
        <legend className="sr-only">Routing settings</legend>
        <fieldset className="grid gap-2">
          <legend className="mb-2 text-sm font-medium">Mode</legend>
          <div className="inline-flex w-fit rounded-lg bg-muted p-[3px]">
            {ROUTING_MODES.map((m) => {
              const allowed = !allowedModes || allowedModes.includes(m)
              return (
                <label
                  key={m}
                  title={allowed ? undefined : "Not enabled for your account"}
                  className={cn(
                    "cursor-pointer rounded-md px-3 py-1 text-sm font-medium text-muted-foreground transition-colors has-checked:bg-background has-checked:text-foreground has-checked:shadow-sm has-disabled:cursor-not-allowed has-disabled:opacity-40 has-focus-visible:ring-3 has-focus-visible:ring-ring/50",
                  )}
                >
                  <input
                    type="radio"
                    className="sr-only"
                    name={`${idPrefix}-mode`}
                    value={m}
                    checked={value.mode === m}
                    disabled={!allowed}
                    onChange={() => onChange({ ...value, mode: m })}
                  />
                  {MODE_INFO[m].label}
                </label>
              )
            })}
          </div>
          <p className="text-sm text-muted-foreground">{MODE_INFO[value.mode].help}</p>
        </fieldset>

        <ul className="grid gap-3" aria-label="Platform rules">
          {ROUTING_PLATFORMS.map((p) => {
            const info = PLATFORM_INFO[p]
            const rule = value.rules[p]
            const Icon = info.icon
            return (
              <li key={p} className="grid gap-2 rounded-lg border p-3">
                <div className="flex items-center justify-between gap-3">
                  <Label htmlFor={`${idPrefix}-${p}-url`} className="flex items-center gap-2">
                    <Icon className="size-4 text-muted-foreground" aria-hidden />
                    {info.label}
                    {!rule.url.trim() && <Badge variant="outline">Uses target</Badge>}
                  </Label>
                  <Switch
                    size="sm"
                    aria-label={`${info.label} rule enabled`}
                    checked={rule.enabled}
                    onCheckedChange={(v) => setRule(p, { enabled: v })}
                  />
                </div>
                <Input
                  id={`${idPrefix}-${p}-url`}
                  inputMode="url"
                  placeholder={info.urlHint}
                  value={rule.url}
                  disabled={!rule.enabled}
                  onChange={(e) => setRule(p, { url: e.target.value })}
                />
                {showApp && p !== "desktop" && (
                  <Input
                    aria-label={`${info.label} app URL`}
                    placeholder="App URL (optional), e.g. myapp://item/42"
                    value={rule.app_url}
                    disabled={!rule.enabled}
                    onChange={(e) => setRule(p, { app_url: e.target.value })}
                  />
                )}
              </li>
            )
          })}
        </ul>
        <p className="text-xs text-muted-foreground">
          {fallbackHint ?? "Visitors on other platforms, bots and link previews go to the link's target."}
        </p>
      </fieldset>
    </div>
  )
}

/** Compact badge for tables. */
export function RoutingBadge({ routing }: { routing?: Routing }) {
  if (!routing) return null
  return (
    <Badge variant={routing.enabled ? "secondary" : "outline"} className="shrink-0" title={`Platform routing: ${MODE_INFO[routing.mode].label}${routing.enabled ? "" : " (off)"}`}>
      <Smartphone aria-hidden />
      <span className="sr-only">Platform routing:</span> {MODE_INFO[routing.mode].label}
    </Badge>
  )
}
