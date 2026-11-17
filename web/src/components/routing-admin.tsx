import { useState, type FormEvent } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Skeleton } from "@/components/ui/skeleton"
import { TextField } from "@/components/controls"
import { RoutingEditor } from "@/components/routing-editor"
import { ApiError, ROUTING_MODES, api, qk, useDomainApps, useMyRouting, type DomainApps, type RoutingMode, type UserRouting } from "@/lib/api"
import { MODE_INFO, draftIsEmpty, draftToInput, routingToDraft } from "@/lib/routing"
import { copyText } from "@/lib/format"

/** Platform routing management UI (TUSK EXTENSION, docs/UNIVERSAL-LINKS.md). */

const errMsg = (e: unknown) => (e instanceof ApiError ? e.message : "An error occurred.")
const lines = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean)

// ---------------------------------------------------------------------------
// App association for a domain (deeplink mode)
// ---------------------------------------------------------------------------

/** id: a domain uuid, or "default" (admins) for DEFAULT_DOMAIN. */
export function DomainAppsDialog({ id, address, open, onOpenChange }: { id: string; address: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { data, isPending } = useDomainApps(id, open)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        {isPending ? (
          <Skeleton className="h-64 w-full" />
        ) : (
          <DomainAppsForm id={id} address={address} data={data ?? null} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DomainAppsForm({ id, address, data, onDone }: { id: string; address: string; data: DomainApps | null; onDone: () => void }) {
  const qc = useQueryClient()
  const [ios, setIos] = useState(() => data?.ios_app_ids.join("\n") ?? "")
  const [pkg, setPkg] = useState(() => data?.android_package ?? "")
  const [sha, setSha] = useState(() => data?.android_sha256.join("\n") ?? "")
  const [paths, setPaths] = useState(() => data?.paths.join("\n") ?? "")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.putDomainApps(id, { ios_app_ids: lines(ios), android_package: pkg.trim(), android_sha256: lines(sha), paths: lines(paths) })
      await qc.invalidateQueries({ queryKey: qk.domainApps(id) })
      toast.success("App association saved")
      onDone()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  const remove = async () => {
    setPending(true)
    try {
      await api.deleteDomainApps(id)
      await qc.invalidateQueries({ queryKey: qk.domainApps(id) })
      toast.success("App association removed")
      onDone()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
        <form onSubmit={save} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>App links for {address}</DialogTitle>
            <DialogDescription>
              Publishes <code>apple-app-site-association</code> and <code>assetlinks.json</code> so installed apps open links on this domain
              (used by the Deep link mode). The app claims every short link on the domain, so a dedicated domain is recommended.
            </DialogDescription>
          </DialogHeader>
            <>
              <div className="grid gap-2">
                <Label htmlFor="apps-ios">iOS app IDs</Label>
                <Textarea id="apps-ios" placeholder="ABCDE12345.com.example.app" value={ios} onChange={(e) => setIos(e.target.value)} />
                <p className="text-xs text-muted-foreground">One per line: Team ID, a dot, then the bundle ID.</p>
              </div>
              <TextField id="apps-android" label="Android package" placeholder="com.example.app" value={pkg} onChange={(e) => setPkg(e.target.value)} />
              <div className="grid gap-2">
                <Label htmlFor="apps-sha">Android signing certificate SHA-256</Label>
                <Textarea id="apps-sha" placeholder="AB:CD:…" className="font-mono text-xs" value={sha} onChange={(e) => setSha(e.target.value)} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="apps-paths">iOS paths (optional)</Label>
                <Textarea id="apps-paths" placeholder="*" value={paths} onChange={(e) => setPaths(e.target.value)} />
                <p className="text-xs text-muted-foreground">Defaults to every short link. Tusk's own pages (/api, /login, …) are always excluded.</p>
              </div>
              {data && (
                <Alert>
                  <AlertDescription className="grid gap-1 text-xs">
                    <span>Published at:</span>
                    {[data.aasa_url, data.assetlinks_url].map((u) => (
                      <button key={u} type="button" className="truncate text-left font-mono underline-offset-4 hover:underline" onClick={() => void copyText(u)}>
                        {u}
                      </button>
                    ))}
                    <span>Apple caches this file through its CDN; changes can take up to a day to reach devices.</span>
                  </AlertDescription>
                </Alert>
              )}
            </>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter className="sm:justify-between">
            {data ? (
              <Button type="button" variant="ghost" className="text-destructive" disabled={pending} onClick={() => void remove()}>
                Remove
              </Button>
            ) : (
              <span />
            )}
            <div className="flex gap-2">
              <Button type="button" variant="outline" onClick={onDone}>
                Cancel
              </Button>
              <Button type="submit" disabled={pending}>
                {pending ? "Saving…" : "Save"}
              </Button>
            </div>
          </DialogFooter>
        </form>
  )
}

// ---------------------------------------------------------------------------
// Admin: per-user allowed modes
// ---------------------------------------------------------------------------

export function UserRoutingDialog({ userId, email, open, onOpenChange }: { userId: number; email: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { data, isPending } = useQuery({
    queryKey: qk.userRouting(userId),
    queryFn: ({ signal }) => api.userRoutingAdmin(userId, signal),
    enabled: open,
  })
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Platform routing access</DialogTitle>
          <DialogDescription className="break-all">{email}</DialogDescription>
        </DialogHeader>
        {isPending || !data ? (
          <Skeleton className="h-32 w-full" />
        ) : (
          <UserRoutingForm userId={userId} data={data} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function UserRoutingForm({ userId, data, onDone }: { userId: number; data: UserRouting; onDone: () => void }) {
  const qc = useQueryClient()
  const [useDefault, setUseDefault] = useState(data.allowed_modes_override == null)
  const [modes, setModes] = useState<RoutingMode[]>(data.allowed_modes_override ?? data.allowed_modes)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const save = async () => {
    setPending(true)
    setError(null)
    try {
      const res = await api.patchUserRoutingAdmin(userId, useDefault ? null : modes)
      await qc.invalidateQueries({ queryKey: qk.userRouting(userId) })
      await qc.invalidateQueries({ queryKey: ["admin", "links"] })
      toast.success(res.disabled_links ? `Saved. Routing turned off on ${res.disabled_links} link(s).` : "Saved")
      onDone()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <>
      <div className="grid gap-4">
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor="ur-default" className="grid gap-0.5">
            <span>Use the instance default</span>
            <span className="text-xs font-normal text-muted-foreground">Set by ROUTING_DEFAULT_MODES.</span>
          </Label>
          <Switch id="ur-default" checked={useDefault} onCheckedChange={setUseDefault} />
        </div>
        <fieldset className="grid gap-3" disabled={useDefault}>
          <legend className="mb-1 text-sm font-medium">Allowed modes</legend>
          {ROUTING_MODES.map((m) => (
            <Label key={m} className="flex items-start gap-2 font-normal">
              <Checkbox
                checked={modes.includes(m)}
                disabled={useDefault}
                onCheckedChange={(c) => setModes((cur) => (c ? [...cur.filter((x) => x !== m), m] : cur.filter((x) => x !== m)))}
              />
              <span className="grid gap-0.5">
                <span className="font-medium">{MODE_INFO[m].label}</span>
                <span className="text-xs text-muted-foreground">{MODE_INFO[m].help}</span>
              </span>
            </Label>
          ))}
        </fieldset>
        <p className="text-xs text-muted-foreground">Removing a mode turns routing off on this user's links that use it. Their rules are kept.</p>
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button disabled={pending} onClick={() => void save()}>
          {pending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </>
  )
}

// ---------------------------------------------------------------------------
// Settings: the user's default routing
// ---------------------------------------------------------------------------

export function RoutingDefaultsForm() {
  const { data, isPending, error: loadError } = useMyRouting()
  if (isPending) return <Skeleton className="h-40 w-full" />
  if (loadError || !data) return <p className="text-sm text-muted-foreground">Could not load routing settings.</p>
  if (data.allowed_modes.length === 0) {
    return <p className="text-sm text-muted-foreground">Platform routing isn't enabled for your account. Ask an administrator.</p>
  }
  return <RoutingDefaultsEditor data={data} />
}

function RoutingDefaultsEditor({ data }: { data: UserRouting }) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState(() => routingToDraft(data.defaults))
  const [autoApply, setAutoApply] = useState(data.auto_apply)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.putMyRouting({ defaults: draftIsEmpty(draft) ? null : draftToInput(draft), auto_apply: autoApply })
      await qc.invalidateQueries({ queryKey: qk.myRouting })
      toast.success("Routing defaults saved")
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <form onSubmit={save} className="grid gap-4" noValidate>
      <RoutingEditor
        idPrefix="defaults"
        value={draft}
        onChange={setDraft}
        allowedModes={data.allowed_modes}
        fallbackHint="Platforms left empty use each link's target."
      />
      <div className="flex items-center justify-between gap-3 rounded-lg border p-3">
        <Label htmlFor="defaults-auto" className="grid gap-0.5">
          <span>Apply to new links</span>
          <span className="text-xs font-normal text-muted-foreground">Links created without routing (including via the API) get these rules.</span>
        </Label>
        <Switch id="defaults-auto" checked={autoApply} onCheckedChange={setAutoApply} />
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div>
        <Button type="submit" disabled={pending}>
          {pending ? "Saving…" : "Save defaults"}
        </Button>
      </div>
    </form>
  )
}
