/**
 * Qubes Air Console - remote desktop application menu.
 *
 * GET /qubes/:id/appmenus forwards the text the remote's qubes.GetAppmenus
 * (remote/qubes-rpc/qubes.GetAppmenus) prints: one `<id>:<Key>=<Value>` line per
 * .desktop field. That text comes from inside the qube, so it is untrusted here:
 * it is parsed for display only, its Exec lines are never used, and the only
 * thing ever sent back is an id that passes the same allowlist the backend and
 * the remote qubes.StartApp apply.
 */

/** A desktop entry exposed by the remote qube's GetAppmenus service. */
export interface DesktopApp {
  id: string;
  name: string;
  comment?: string;
}

// Mirrors service.ValidAppID (console/backend/internal/service/qube_service.go)
// and the case pattern in remote/qubes-rpc/qubes.StartApp.
const validAppId = /^[A-Za-z0-9._+-]{1,128}$/;

// An id made only of dots passes the character allowlist but is a URL path
// dot-segment: `/apps/../launch` is normalised by the browser into a request
// for a different endpoint. No .desktop basename looks like that.
const dotSegment = /^\.+$/;

/**
 * Upper bound on listed applications. A qube with a real desktop has tens; the
 * cap keeps a hostile or broken menu from filling the dialog with thousands of
 * rows.
 */
export const MAX_APPS = 256;

const localizedName = /^Name\[[^\]]+\]$/;

/** Reports whether an id may be sent back as the launch argument. */
export function isLaunchableAppId(id: string): boolean {
  return validAppId.test(id) && !dotSegment.test(id);
}

interface Draft {
  name?: string;
  localizedName?: string;
  comment?: string;
}

/** Splits `<id>:<Key>=<Value>`, or returns null for anything else. */
function parseLine(line: string): { id: string; key: string; value: string } | null {
  const separator = line.indexOf(':');
  if (separator <= 0) return null;
  const id = line.slice(0, separator);
  if (!isLaunchableAppId(id)) return null;
  const field = line.slice(separator + 1);
  const equals = field.indexOf('=');
  if (equals <= 0) return null;
  return { id, key: field.slice(0, equals), value: field.slice(equals + 1).trim() };
}

function applyField(draft: Draft, key: string, value: string): void {
  if (!value) return;
  if (key === 'Name') draft.name ??= value;
  else if (localizedName.test(key)) draft.localizedName ??= value;
  else if (key === 'Comment') draft.comment ??= value;
}

/**
 * Parses the remote line protocol without trusting or executing its Exec field.
 *
 * Entries without a display name are dropped: an id alone is not something an
 * operator can recognise. The unlocalised `Name` wins over `Name[xx]`, whatever
 * order the lines arrive in.
 */
export function parseAppMenus(raw: string): DesktopApp[] {
  const drafts = new Map<string, Draft>();
  for (const line of raw.split(/\r?\n/)) {
    const parsed = parseLine(line);
    if (!parsed) continue;
    let draft = drafts.get(parsed.id);
    if (!draft) {
      if (drafts.size >= MAX_APPS) continue;
      draft = {};
      drafts.set(parsed.id, draft);
    }
    applyField(draft, parsed.key, parsed.value);
  }

  const apps: DesktopApp[] = [];
  for (const [id, draft] of drafts) {
    const name = draft.name ?? draft.localizedName;
    if (!name) continue;
    apps.push(draft.comment ? { id, name, comment: draft.comment } : { id, name });
  }
  return apps;
}

/**
 * What the remote qubes.StartApp answered, read for the operator.
 *
 * - launched: the reply is the service's own success line for this app id.
 * - refused: the reply is one of the service's refusal lines.
 * - unrecognised: anything else (another agent build, an empty or truncated
 *   reply). Never read as a launch: saying "started" for a reply nobody
 *   understood is the failure this reader exists to avoid.
 */
export interface LaunchOutcome {
  result: 'launched' | 'refused' | 'unrecognised';
  detail: string;
}

// remote/qubes-rpc/qubes.StartApp always exits 0 so its stdout reaches the
// caller; whether the app started is in that text, not in the HTTP status.
// Its only success output is one line naming the app and the display.
const startAppLaunched = /^qubes\.StartApp: launched '([^'\n]*)' on [^\s]+$/;
// Its three refusal prefixes.
const startAppRefusal = /^qubes\.StartApp: (failed to launch|refusing|missing app id)/;

/** Longest reply shown verbatim; the text is the remote's, not the console's. */
const MAX_DETAIL = 300;

/**
 * Reads qubes.StartApp's reply to a launch of `appId`. A 200 from the launch
 * endpoint only means the request reached the qube.
 */
export function readLaunchReply(reply: string, appId: string): LaunchOutcome {
  const text = reply.trim();
  const detail = text.slice(0, MAX_DETAIL);
  const launched = startAppLaunched.exec(text);
  if (launched && launched[1] === appId) return { result: 'launched', detail };
  if (startAppRefusal.test(text)) return { result: 'refused', detail };
  return { result: 'unrecognised', detail };
}
