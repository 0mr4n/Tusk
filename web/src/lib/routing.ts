import { Monitor, Smartphone, TabletSmartphone, type LucideIcon } from "lucide-react"

import { ROUTING_PLATFORMS, type Routing, type RoutingInput, type RoutingMode, type RoutingPlatform } from "@/lib/api"

/**
 * Platform routing form model (TUSK EXTENSION, docs/UNIVERSAL-LINKS.md):
 * a RoutingDraft is what the editor edits; draftToInput() turns it into the API payload.
 */

export interface RuleDraft {
  enabled: boolean
  url: string
  app_url: string
}

export interface RoutingDraft {
  enabled: boolean
  mode: RoutingMode
  rules: Record<RoutingPlatform, RuleDraft>
}

export const MODE_INFO: Record<RoutingMode, { label: string; help: string }> = {
  redirect: { label: "Redirect", help: "Send each platform straight to its URL." },
  deeplink: {
    label: "Deep link",
    help: "Like redirect, plus Universal Links / App Links: an installed app opens the short link directly (set up the app association on the domain).",
  },
  smart: {
    label: "Smart",
    help: "Show a short page that tries to open the app, then falls back to the URL. Handles iPads and in-app browsers.",
  },
}

export const PLATFORM_INFO: Record<RoutingPlatform, { label: string; icon: LucideIcon; urlHint: string }> = {
  ios: { label: "iOS / iPadOS", icon: TabletSmartphone, urlHint: "https://apps.apple.com/app/id…" },
  android: { label: "Android", icon: Smartphone, urlHint: "https://play.google.com/store/apps/details?id=…" },
  desktop: { label: "Desktop", icon: Monitor, urlHint: "https://example.com/…" },
}

export function emptyDraft(): RoutingDraft {
  const rules = {} as Record<RoutingPlatform, RuleDraft>
  for (const p of ROUTING_PLATFORMS) rules[p] = { enabled: true, url: "", app_url: "" }
  return { enabled: true, mode: "redirect", rules }
}

export function routingToDraft(r: Routing | null | undefined): RoutingDraft {
  const d = emptyDraft()
  if (!r) return d
  d.enabled = r.enabled
  d.mode = r.mode
  for (const p of ROUTING_PLATFORMS) {
    const rule = r.rules[p]
    if (rule) d.rules[p] = { enabled: rule.enabled, url: rule.url, app_url: rule.app_url ?? "" }
  }
  return d
}

/** True when no platform has a URL, i.e. there is nothing to save. */
export function draftIsEmpty(d: RoutingDraft): boolean {
  return ROUTING_PLATFORMS.every((p) => !d.rules[p].url.trim())
}

/** Platforms without a URL are omitted; the API falls back to the link target for them. */
export function draftToInput(d: RoutingDraft): RoutingInput {
  const rules: RoutingInput["rules"] = {}
  for (const p of ROUTING_PLATFORMS) {
    const r = d.rules[p]
    if (!r.url.trim()) continue
    rules[p] = { enabled: r.enabled, url: r.url.trim(), app_url: d.mode === "redirect" ? null : r.app_url.trim() || null }
  }
  return { enabled: d.enabled, mode: d.mode, rules }
}
