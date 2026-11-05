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

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { UserResourcesConfig } from './user-resources-schema'

type Limits = UserResourcesConfig['defaults']
const resources = ['concurrency', 'input_tpm', 'rpm'] as const

export function ResourceLimitFields(props: {
  limits: Partial<Limits>
  stacked?: boolean
  defaults?: Limits
  onChange: (limits: Partial<Limits>) => void
}) {
  const { t, i18n } = useTranslation()
  const id = useId()
  const labels = {
    concurrency: t('Concurrency'),
    input_tpm: t('Estimated input TPM'),
    rpm: t('RPM'),
  }
  return (
    <div className={props.stacked ? 'space-y-4' : 'grid gap-4 md:grid-cols-3'}>
      {resources.map((resource) => {
        const limit = props.limits[resource]
        let mode = 'inherit'
        if (limit) mode = limit.enabled ? 'limited' : 'unlimited'
        const inherited = props.defaults?.[resource]
        return (
          <div key={resource} className='space-y-2'>
            <Label htmlFor={`${id}-${resource}`}>{labels[resource]}</Label>
            <NativeSelect
              id={`${id}-${resource}`}
              value={mode}
              onChange={(event) => {
                const next = { ...props.limits }
                if (event.target.value === 'inherit') delete next[resource]
                else {
                  next[resource] = {
                    enabled: event.target.value === 'limited',
                    limit: limit?.limit ?? inherited?.limit ?? 0,
                  }
                }
                props.onChange(next)
              }}
            >
              {props.defaults && (
                <NativeSelectOption value='inherit'>
                  {t('Inherit default')}
                </NativeSelectOption>
              )}
              <NativeSelectOption value='unlimited'>
                {t('Unlimited')}
              </NativeSelectOption>
              <NativeSelectOption value='limited'>
                {t('Limited')}
              </NativeSelectOption>
            </NativeSelect>
            {limit?.enabled && (
              <Input
                type='number'
                min={0}
                max={2147483647}
                step={1}
                aria-label={`${labels[resource]} ${t('Limit')}`}
                value={limit.limit}
                aria-invalid={
                  !Number.isInteger(limit.limit) ||
                  limit.limit < 0 ||
                  limit.limit > 2147483647
                }
                onChange={(event) => {
                  const value = event.target.valueAsNumber
                  if (Number.isFinite(value)) {
                    props.onChange({
                      ...props.limits,
                      [resource]: { ...limit, limit: value },
                    })
                  }
                }}
              />
            )}
            {!limit && inherited && (
              <p className='text-muted-foreground text-sm'>
                {inherited.enabled
                  ? `${t('Limit')}: ${formatNumber(inherited.limit, toIntlLocale(i18n.resolvedLanguage || i18n.language))}`
                  : t('Unlimited')}
              </p>
            )}
          </div>
        )
      })}
    </div>
  )
}
