import { createInstance } from 'i18next'
import { expect, it } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

it('shows the diagnostic title and raw-body warning in the selected language', async () => {
  const i18n = createInstance()
  await i18n.init({
    lng: 'zh',
    fallbackLng: 'en',
    resources: { en, zh, 'zh-TW': zhTW, fr, ja, ru, vi },
  })
  expect(i18n.t('Affinity diagnostics')).toBe('渠道亲和诊断')
  expect(
    i18n.t(
      'Complete JSON bodies may contain sensitive data. They are stored unchanged and are accessible to administrators for one hour from task creation.'
    )
  ).toContain('将原样保存')
  for (const [language, title] of Object.entries({
    en: 'Affinity diagnostics',
    'zh-TW': '通道親和診斷',
    fr: 'Diagnostic d’affinité',
    ja: 'アフィニティ診断',
    ru: 'Диагностика привязки',
    vi: 'Chẩn đoán liên kết kênh',
  })) {
    await i18n.changeLanguage(language)
    expect(i18n.t('Affinity diagnostics')).toBe(title)
  }
})
