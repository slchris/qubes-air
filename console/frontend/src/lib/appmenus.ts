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
// and remote/qubes-rpc/qubes.StartApp. The 15-byte service prefix shares the
// Relay/agent's 128-byte limit with this ASCII id.
const invalidAppIdCharacter = /[^A-Za-z0-9._+-]/;

/**
 * Upper bound on listed applications. A qube with a real desktop has tens; the
 * cap keeps a hostile or broken menu from filling the dialog with thousands of
 * rows.
 */
export const MAX_APPS = 256;

const localizedName = /^Name\[[^\]]+\]$/;

/** Reports whether an id may be sent back as the launch argument. */
export function isLaunchableAppId(id: string): boolean {
  return id.length >= 1 && id.length <= 113 && !invalidAppIdCharacter.test(id) && id !== '.' && !id.includes('..');
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
 * - unrecognised: anything else (another agent build, an empty or truncated
 *   reply). Never read as a launch: saying "started" for a reply nobody
 *   understood is the failure this reader exists to avoid.
 */
export interface LaunchOutcome {
  result: 'launched' | 'unrecognised';
  detail: string;
}

// Nonzero service exits become HTTP errors. Success only confirms that the
// launch helper accepted the request; it does not prove a window is visible.
const startAppLaunched = /^qubes\.StartApp: launched '([^'\n]*)' on :[0-9]{1,5}(?:\.[0-9]{1,2})?$/;

/** Longest reply shown verbatim; the text is the remote's, not the console's. */
const MAX_DETAIL = 300;

/**
 * Reads qubes.StartApp's reply to a launch of `appId`. A 200 from the launch
 * endpoint still needs a recognised acknowledgement for this exact app.
 */
export function readLaunchReply(reply: string, appId: string): LaunchOutcome {
  const text = reply.trim();
  const detail = text.slice(0, MAX_DETAIL);
  const launched = startAppLaunched.exec(text);
  if (launched && launched[1] === appId && isLaunchableAppId(appId)) return { result: 'launched', detail };
  return { result: 'unrecognised', detail };
}
