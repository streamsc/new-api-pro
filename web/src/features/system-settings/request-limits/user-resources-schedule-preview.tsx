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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  minutesPerDay,
  resourceScheduleMinute,
  resourceScheduleSpans,
  scaleModelResourceLimit,
  type ModelResourceRule,
  type ResourceSchedule,
} from './user-resources-schema'

function clockTime(minutes: number): string {
  return `${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`
}

export function ModelScheduleTimeline(props: { schedule: ResourceSchedule }) {
  const { t } = useTranslation()
  const labelId = useId()
  const spans = props.schedule.periods
    .flatMap((period, index) => {
      const start = resourceScheduleMinute(period.start)
      const end = resourceScheduleMinute(period.end)
      if (start === undefined || end === undefined) return []
      return resourceScheduleSpans(start, end).map(([from, to]) => ({
        from,
        to,
        number: index + 1,
      }))
    })
    .sort((a, b) => a.from - b.from)
  const baseSpans: { from: number; to: number; number?: number }[] = []
  let coveredUntil = 0
  for (const span of spans) {
    if (span.from > coveredUntil) {
      baseSpans.push({ from: coveredUntil, to: span.from })
    }
    coveredUntil = Math.max(coveredUntil, span.to)
  }
  if (coveredUntil < minutesPerDay) {
    baseSpans.push({ from: coveredUntil, to: minutesPerDay })
  }

  return (
    <figure className='space-y-2' aria-labelledby={labelId}>
      <figcaption
        id={labelId}
        className='flex flex-wrap items-center gap-x-2 text-sm font-medium'
      >
        {t('24-hour timeline')}
        <span className='text-muted-foreground text-xs font-normal break-all'>
          {props.schedule.timezone}
        </span>
      </figcaption>
      <div className='relative h-4 overflow-hidden rounded-sm'>
        {[...baseSpans, ...spans].map(({ from, to, number }) => {
          const label =
            number === undefined
              ? t('Base limits')
              : t('Time period {{number}}', { number })
          const description = `${label} ${clockTime(from)}–${clockTime(to)}`
          return (
            <span
              key={`${number ?? 'base'}-${from}`}
              role='img'
              aria-label={description}
              title={description}
              className={cn(
                'absolute inset-y-0 border-e border-background last:border-e-0',
                number === undefined ? 'bg-muted' : 'bg-primary'
              )}
              style={{
                left: `${(from / minutesPerDay) * 100}%`,
                width: `${((to - from) / minutesPerDay) * 100}%`,
              }}
            />
          )
        })}
      </div>
      <div
        className='text-muted-foreground flex justify-between text-xs tabular-nums'
        aria-hidden='true'
      >
        <span>00:00</span>
        <span>06:00</span>
        <span>12:00</span>
        <span>18:00</span>
        <span>24:00</span>
      </div>
      <div className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
        <span className='inline-flex items-center gap-1.5'>
          <span
            className='bg-muted size-2.5 rounded-xs border'
            aria-hidden='true'
          />
          {t('Base limits')}
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='bg-primary size-2.5 rounded-xs' aria-hidden='true' />
          {t('Scheduled limits')}
        </span>
      </div>
    </figure>
  )
}

export function ModelScheduleLimitPreview(props: {
  defaults: ModelResourceRule['defaults']
  multiplier: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const title = t('Model default limits preview')
  const resources = [
    { label: t('Concurrency'), limit: props.defaults.concurrency },
    { label: t('Estimated input TPM'), limit: props.defaults.input_tpm },
    { label: t('RPM'), limit: props.defaults.rpm },
  ]
  return (
    <div role='group' aria-label={title} className='space-y-1 text-xs'>
      <p className='text-muted-foreground font-medium'>{title}</p>
      <dl className='flex flex-wrap gap-x-5 gap-y-1'>
        {resources.map(({ label, limit }) => (
          <div key={label} className='flex items-baseline gap-1.5'>
            <dt className='text-muted-foreground'>{label}</dt>
            <dd className='font-medium tabular-nums'>
              {limit.enabled
                ? formatNumber(
                    scaleModelResourceLimit(limit.limit, props.multiplier),
                    locale
                  )
                : t('Unlimited')}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
