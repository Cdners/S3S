// A pack may translate any subset of the built-in catalogue.
export interface LanguagePack {
  locale: string
  name: string
  messages: Record<string, string>
}

export function parameters(message: string): string[] {
  return [...message.matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)].map(match => match[1]).sort()
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

export function parsePack(value: unknown, fallback: Record<string, string>): LanguagePack {
  if (!record(value) || typeof value.locale !== 'string' || typeof value.name !== 'string'
      || !value.name.trim() || value.name.length > 80 || !record(value.messages)) throw Error('invalidPack')
  const [locale] = Intl.getCanonicalLocales(value.locale)
  if (!locale || locale === 'zh-CN' || locale === 'en-US') throw Error('reservedLocale')
  const messages: Record<string, string> = Object.create(null)
  for (const [key, translation] of Object.entries(value.messages)) {
    if (!Object.hasOwn(fallback, key) || typeof translation !== 'string' || !translation.trim()
        || parameters(translation).join('|') !== parameters(fallback[key]).join('|')) throw Error('invalidPack')
    messages[key] = translation
  }
  if (!Object.keys(messages).length) throw Error('invalidPack')
  return { locale, name: value.name.trim(), messages }
}

export function translate(key: string, selected: Record<string, string>, fallback: Record<string, string>,
                          values: Record<string, string | number> = {}) {
  const message = selected[key] ?? fallback[key] ?? key
  return message.replace(/\{([A-Za-z][A-Za-z0-9_]*)\}/g, (token, name) => String(values[name] ?? token))
}
