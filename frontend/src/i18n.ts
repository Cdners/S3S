import { computed, ref, watchEffect } from 'vue'
import en from './locales/en-US.json'
import zh from './locales/zh-CN.json'
import { parsePack, translate, type LanguagePack } from './catalog'

export type MessageKey = keyof typeof en
const builtins: Record<string, Record<string, string>> = { 'zh-CN': zh, 'en-US': en }
const packKey = 's3s.languagePacks'
const localeKey = 's3s.locale'

function readSetting(key: string) {
  try { return localStorage.getItem(key) } catch { return null }
}
function readPacks(): LanguagePack[] {
  try {
    const saved: unknown = JSON.parse(readSetting(packKey) ?? '[]')
    return Array.isArray(saved) ? saved.map(pack => parsePack(pack, en)) : []
  } catch { return [] }
}
const packs = ref(readPacks())
export const builtinLanguages = Object.entries(builtins).map(([locale, messages]) => ({
  locale, name: messages['language.nativeName']
}))
export const importedLanguages = computed(() => packs.value.map(({ locale, name }) => ({ locale, name })))
const available = computed(() => [...builtinLanguages, ...importedLanguages.value])
const savedLocale = readSetting(localeKey)
export const locale = ref(available.value.some(item => item.locale === savedLocale) ? savedLocale! : 'zh-CN')
const selected = computed(() => builtins[locale.value] ?? packs.value.find(item => item.locale === locale.value)?.messages ?? en)
export const t = (key: MessageKey, values?: Record<string, string | number>) => translate(key, selected.value, en, values)

export function setLanguage(code: string) {
  if (!available.value.some(item => item.locale === code)) return
  locale.value = code
  try { localStorage.setItem(localeKey, code) } catch { /* Session selection remains available. */ }
}

export async function importLanguage(file: File) {
  if (file.size > 256 * 1024) throw Error('invalidPack')
  const pack = parsePack(JSON.parse(await file.text()), en)
  const updated = [...packs.value.filter(item => item.locale !== pack.locale), pack]
  localStorage.setItem(packKey, JSON.stringify(updated))
  packs.value = updated
  setLanguage(pack.locale)
}

watchEffect(() => {
  document.documentElement.lang = locale.value
  document.title = t('app.name')
})
