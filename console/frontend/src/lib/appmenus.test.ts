import { describe, expect, it } from 'vitest'

import { MAX_APPS, isLaunchableAppId, parseAppMenus, readLaunchReply } from './appmenus'

describe('parseAppMenus', () => {
  it('groups desktop entry fields and uses the identifier only as a launch key', () => {
    const apps = parseAppMenus([
      'firefox.desktop:Exec=qubes-desktop-run firefox.desktop',
      'firefox.desktop:Name=Firefox',
      'firefox.desktop:Comment=Web browser',
      'org.gnome.Terminal.desktop:Name[de]=Terminal (de)',
      'org.gnome.Terminal.desktop:Name=Terminal',
    ].join('\n'))

    expect(apps).toEqual([
      { id: 'firefox.desktop', name: 'Firefox', comment: 'Web browser' },
      { id: 'org.gnome.Terminal.desktop', name: 'Terminal' },
    ])
  })

  it('falls back to a localized name when there is no plain one', () => {
    expect(parseAppMenus('gedit.desktop:Name[en_GB]=Text Editor\r\n')).toEqual([
      { id: 'gedit.desktop', name: 'Text Editor' },
    ])
  })

  it('rejects unsafe identifiers and entries without a display name', () => {
    const apps = parseAppMenus([
      '../launch.desktop:Name=Unsafe',
      'bad name.desktop:Name=Unsafe',
      'semi;colon.desktop:Name=Unsafe',
      `${'a'.repeat(129)}:Name=Too long`,
      'hidden.desktop:Exec=qubes-desktop-run hidden.desktop',
      'blank.desktop:Name=   ',
      ':Name=No id',
      'no-field.desktop',
      'no-key.desktop:=value',
      'safe.desktop:Name=Safe',
    ].join('\n'))

    expect(apps).toEqual([{ id: 'safe.desktop', name: 'Safe' }])
  })

  it('never offers a dot-segment id that the browser would rewrite into another path', () => {
    const apps = parseAppMenus(['.:Name=Dot', '..:Name=Up', '...:Name=Dots', 'a..b:Name=Fine'].join('\n'))

    expect(apps).toEqual([{ id: 'a..b', name: 'Fine' }])
  })

  it('caps the number of listed applications', () => {
    const raw = Array.from({ length: MAX_APPS + 50 }, (_, i) => `app${i}.desktop:Name=App ${i}`).join('\n')

    const apps = parseAppMenus(raw)

    expect(apps).toHaveLength(MAX_APPS)
    expect(apps[0].id).toBe('app0.desktop')
  })

  it('returns nothing for an empty menu', () => {
    expect(parseAppMenus('')).toEqual([])
  })
})

describe('isLaunchableAppId', () => {
  it.each(['firefox.desktop', 'org.gnome.Terminal', 'c++-ide', 'a'.repeat(128)])('accepts %s', (id) => {
    expect(isLaunchableAppId(id)).toBe(true)
  })

  // Percent-encoded forms are refused as they arrive: '%' is outside the
  // allowlist, so an id cannot smuggle a slash or a dot-segment past it and
  // have the server decode it later.
  it.each([
    '', '.', '..', 'a/b', 'a b', 'a\nb', '$(id)', 'a'.repeat(129),
    '%2e%2e', 'a%2fb', '%', 'a:b', 'é',
  ])('refuses %j', (id) => {
    expect(isLaunchableAppId(id)).toBe(false)
  })
})

describe('readLaunchReply', () => {
  it('reads the success line for the requested app as launched', () => {
    expect(readLaunchReply("qubes.StartApp: launched 'firefox.desktop' on :100\n", 'firefox.desktop')).toEqual({
      result: 'launched',
      detail: "qubes.StartApp: launched 'firefox.desktop' on :100",
    })
  })

  // The remote script always exits 0, so these arrive as HTTP 200.
  it.each([
    "qubes.StartApp: failed to launch 'x.desktop' on :100: no such file",
    "qubes.StartApp: refusing suspicious app id 'x y'",
    'qubes.StartApp: missing app id (pass it as the qrexec service argument)',
  ])('reads %j as refused', (reply) => {
    expect(readLaunchReply(reply, 'x.desktop').result).toBe('refused')
  })

  // Only the known success line counts as a launch. Anything else is reported
  // as not understood, never as started.
  it.each([
    ['an empty reply', ''],
    ['another program', 'OK'],
    ['a success line for a different app', "qubes.StartApp: launched 'other.desktop' on :100"],
    ['a success line with trailing output', "qubes.StartApp: launched 'x.desktop' on :100\nand more"],
    ['a success line missing its display', "qubes.StartApp: launched 'x.desktop'"],
    ['a prefix the service does not print', 'qubes.StartApp: launching x.desktop'],
  ])('reads %s as unrecognised', (_name, reply) => {
    expect(readLaunchReply(reply, 'x.desktop').result).toBe('unrecognised')
  })

  it('bounds how much remote text it repeats', () => {
    expect(readLaunchReply('x'.repeat(5000), 'x.desktop').detail).toHaveLength(300)
  })
})
