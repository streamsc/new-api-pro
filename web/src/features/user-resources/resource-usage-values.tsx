/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'

import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { resourceSourceKeys, type ResourceSnapshot } from './types'

export function ResourceUsageValues(props: {
  snapshot: ResourceSnapshot
  resource: 'concurrency' | 'input_tpm' | 'rpm'
  showSources?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const limit = props.snapshot.limits[props.resource]
  let cap = t('Unlimited')
  if (limit.enabled) {
    cap = limit.limit === 0 ? t('Blocked') : formatNumber(limit.limit, locale)
  }
  let value = t('Unknown')
  if (props.snapshot.available && props.snapshot.usage) {
    value = formatNumber(props.snapshot.usage[props.resource], locale)
  }
  const source = props.snapshot.sources?.[props.resource]
  return (
    <div className='space-y-1'>
      <div>
        {value} / {cap}
      </div>
      {props.showSources && source && (
        <div className='text-muted-foreground text-xs'>
          {t(resourceSourceKeys[source] ?? source)}
        </div>
      )}
    </div>
  )
}

export function ResourceUsageMeter(props: {
  snapshot: ResourceSnapshot
  resource: 'concurrency' | 'input_tpm' | 'rpm'
  label: string
  showLabel?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const limit = props.snapshot.limits[props.resource]
  const usage = props.snapshot.available
    ? props.snapshot.usage?.[props.resource]
    : undefined
  let percent: number | null = null
  let status = 'Unlimited'
  let tone = 'text-muted-foreground'
  if (limit.enabled && usage !== undefined) {
    percent = limit.limit === 0 ? 100 : (usage / limit.limit) * 100
    status = limit.limit === 0 ? 'Blocked' : 'Normal'
    tone = 'text-success [&_[data-slot=progress-indicator]]:bg-success'
    if (percent >= 80) {
      if (limit.limit > 0) status = 'Near or at limit'
      tone =
        'text-destructive [&_[data-slot=progress-indicator]]:bg-destructive'
    } else if (percent >= 40) {
      status = 'Elevated usage'
      tone =
        'text-[color-mix(in_oklch,var(--warning)_65%,var(--foreground))] dark:text-warning [&_[data-slot=progress-indicator]]:bg-warning'
    }
  }
  return (
    <div className={tone}>
      <div className='flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs'>
        {props.showLabel && (
          <span className='text-foreground font-medium'>{props.label}</span>
        )}
        {usage === undefined ? (
          <ResourceUsageValues
            snapshot={props.snapshot}
            resource={props.resource}
          />
        ) : (
          <span>
            {formatNumber(usage, locale)} /{' '}
            {limit.enabled ? formatNumber(limit.limit, locale) : t('Unlimited')}
            {percent !== null &&
              ` · ${t(status)} · ${formatNumber(percent, locale)}%`}
          </span>
        )}
      </div>
      {percent !== null && (
        <Progress
          className='mt-1'
          value={Math.min(percent, 100)}
          aria-label={props.label}
        />
      )}
    </div>
  )
}
