/**
 * Qubes Air Console - which zone providers this console can provision into.
 *
 * The backend is the authority: it creates a zone only when a provider adapter
 * is registered for its type, and answers 422 otherwise. Today that is Proxmox
 * alone. This list mirrors that answer so the UI never offers a choice the API
 * will refuse, and so every form and card says "not implemented" the same way.
 *
 * Flip `implemented` only once the backend registers an adapter for the type
 * AND it has passed its own real-provider acceptance (AGENTS.md §1).
 */
import type { ZoneType } from './types';

export interface ZoneProvider {
  value: ZoneType;
  label: string;
  implemented: boolean;
}

export const ZONE_PROVIDERS: readonly ZoneProvider[] = [
  { value: 'proxmox', label: 'Proxmox', implemented: true },
  { value: 'gcp', label: 'Google Cloud', implemented: false },
  { value: 'aws', label: 'AWS', implemented: false },
  { value: 'azure', label: 'Azure', implemented: false },
];

/** What a form starts on. Must be an implemented provider. */
export const DEFAULT_ZONE_PROVIDER: ZoneType = 'proxmox';

/** The wording every view uses for a provider with no adapter. */
export const NOT_IMPLEMENTED = 'not implemented';

/** True only for a provider type the console can provision into. */
export function isProviderImplemented(type: string): boolean {
  return ZONE_PROVIDERS.some(p => p.value === type && p.implemented);
}

/**
 * True for a provider type the console knows but cannot provision into. False
 * for types that are not providers at all (an SSH key, an API key), which are
 * neither implemented nor unimplemented.
 */
export function isUnimplementedProvider(type: string): boolean {
  return ZONE_PROVIDERS.some(p => p.value === type && !p.implemented);
}

/** The label for a picker option: unimplemented providers say so. */
export function providerOptionLabel(p: ZoneProvider): string {
  return p.implemented ? p.label : `${p.label} (${NOT_IMPLEMENTED})`;
}

/** Labels of the providers that cannot be chosen, for an explanatory note. */
export function unimplementedProviderLabels(): string[] {
  return ZONE_PROVIDERS.filter(p => !p.implemented).map(p => p.label);
}
