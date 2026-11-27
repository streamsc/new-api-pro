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
import { useQuery } from '@tanstack/react-query'
import { nanoid } from 'nanoid'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { getEnabledModels } from '@/features/channels/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { DraftNumberInput } from '../models/draft-number-input'
import {
  ModelScheduleLimitPreview,
  ModelScheduleTimeline,
} from './user-resources-schedule-preview'
import {
  exactModelSchema,
  modelRuleSchema,
  resourceScheduleMinute,
  unlimitedResourceLimits,
  type ModelResourceRule,
  type ResourceSchedule,
  type ResourceSchedulePeriod,
  type UserResourcesConfig,
} from './user-resources-schema'

const timezones = [...new Set(['UTC', ...Intl.supportedValuesOf('timeZone')])]
const timezoneOptions = timezones.map((zone) => ({ value: zone, label: zone }))

export function UserResourcesScheduleEditor(props: {
  config: UserResourcesConfig
  model: string
  onModelChange: (model: string) => void
  onChange: (config: UserResourcesConfig) => void
}) {
  const { t } = useTranslation()
  const modelId = useId()
  const model = props.model
  const models = props.config.models ?? {}
  const enabledModels = useQuery({
    queryKey: ['resource-schedule-enabled-models'],
    queryFn: async () =>
      requireServerSuccess(await getEnabledModels()).data ?? [],
    meta: { errorToast: false },
  })
  const options = useMemo(() => {
    const configured = Object.keys(props.config.models ?? {})
    return [...new Set([...configured, ...(enabledModels.data ?? [])])]
      .sort()
      .map((name) => ({ value: name, label: name }))
  }, [props.config.models, enabledModels.data])
  const validModel = exactModelSchema.safeParse(model).success
  const invalidModel = model !== '' && !validModel
  const rule = Object.hasOwn(models, model)
    ? models[model]
    : { defaults: unlimitedResourceLimits, users: {} }

  return (
    <section
      className='min-w-0 space-y-5'
      aria-label={t('Model rate limit schedules')}
    >
      <div className='max-w-md space-y-2'>
        <Label htmlFor={modelId}>{t('Model')}</Label>
        <ComboboxInput
          id={modelId}
          options={options}
          value={model}
          onValueChange={props.onModelChange}
          placeholder={t('Exact model name')}
          aria-invalid={invalidModel}
          allowCustomValue
        />
        {invalidModel && (
          <p className='text-destructive text-sm' role='alert'>
            {t('Enter a new valid exact model name.')}
          </p>
        )}
      </div>
      {validModel && (
        <ModelScheduleFields
          key={model}
          rule={rule}
          onChange={(next) =>
            props.onChange({
              ...props.config,
              models: { ...models, [model]: next },
            })
          }
        />
      )}
    </section>
  )
}

function ModelScheduleFields(props: {
  rule: ModelResourceRule
  onChange: (rule: ModelResourceRule) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const { schedule } = props.rule
  const [rowKeys, setRowKeys] = useState<string[]>([])
  const rows = schedule?.periods.map((period, index) => ({
    period,
    key: rowKeys[index] ?? `${id}-loaded-${index}`,
  }))
  const checked = modelRuleSchema.safeParse(props.rule)
  const issues = checked.success ? [] : checked.error.issues
  const timezoneError = issues.find(
    (issue) => issue.path[0] === 'schedule' && issue.path[1] === 'timezone'
  )
  const options =
    schedule?.timezone && !timezones.includes(schedule.timezone)
      ? [
          ...timezoneOptions,
          { value: schedule.timezone, label: schedule.timezone },
        ]
      : timezoneOptions

  function update(next: ResourceSchedule) {
    const rule = { ...props.rule }
    if (next.periods.length) rule.schedule = next
    else delete rule.schedule
    props.onChange(rule)
  }

  return (
    <div className='space-y-4 border-t pt-4'>
      <div className='flex flex-wrap items-end gap-3'>
        {schedule && (
          <div className='min-w-0 grow space-y-2 sm:max-w-xs'>
            <Label htmlFor={`${id}-zone`}>{t('Timezone')}</Label>
            <ComboboxInput
              id={`${id}-zone`}
              options={options}
              value={schedule.timezone}
              onValueChange={(timezone) => update({ ...schedule, timezone })}
              openOnFocus={false}
              aria-invalid={!!timezoneError}
            />
            {timezoneError && (
              <p className='text-destructive text-sm' role='alert'>
                {t('Choose a valid IANA timezone.')}
              </p>
            )}
          </div>
        )}
        <Button
          type='button'
          variant='outline'
          onClick={() => {
            setRowKeys([...(rows?.map(({ key }) => key) ?? []), nanoid()])
            update({
              timezone: schedule?.timezone ?? 'UTC',
              periods: [
                ...(schedule?.periods ?? []),
                { start: '07:00', end: '21:00', multiplier: 1 },
              ],
            })
          }}
        >
          {t('Add time period')}
        </Button>
      </div>
      {schedule && <ModelScheduleTimeline schedule={schedule} />}
      {schedule &&
        rows?.map(({ period, key: rowKey }, row) => {
          const rowIssues = issues.filter(
            (issue) => issue.path[0] === 'schedule' && issue.path[2] === row
          )
          const start = resourceScheduleMinute(period.start)
          const end = resourceScheduleMinute(period.end)
          const crossesMidnight =
            start !== undefined && end !== undefined && end < start
          const updatePeriod = (fields: Partial<ResourceSchedulePeriod>) => {
            const periods = [...schedule.periods]
            periods[row] = { ...period, ...fields }
            update({ ...schedule, periods })
          }
          return (
            <fieldset key={rowKey} className='min-w-0 space-y-2 border-t pt-3'>
              <legend className='px-1 text-sm font-medium'>
                {t('Time period {{number}}', { number: row + 1 })}
              </legend>
              <div className='grid min-w-0 gap-3 sm:grid-cols-3'>
                {(['start', 'end', 'multiplier'] as const).map((key) => {
                  const fieldId = `${id}-${row}-${key}`
                  const issue = rowIssues.find((error) => error.path[3] === key)
                  return (
                    <div key={key} className='min-w-0 space-y-2'>
                      <Label htmlFor={fieldId}>
                        {
                          {
                            start: t('Start time'),
                            end: t('End time'),
                            multiplier: t('Multiplier'),
                          }[key]
                        }
                      </Label>
                      {key === 'multiplier' ? (
                        <DraftNumberInput
                          id={fieldId}
                          inputMode='decimal'
                          step='any'
                          value={period.multiplier}
                          aria-invalid={!!issue}
                          aria-describedby={
                            issue ? `${fieldId}-error` : undefined
                          }
                          onValueChange={(multiplier) =>
                            updatePeriod({ multiplier })
                          }
                        />
                      ) : (
                        <Input
                          id={fieldId}
                          type='time'
                          value={period[key]}
                          aria-invalid={!!issue}
                          aria-describedby={
                            issue ? `${fieldId}-error` : undefined
                          }
                          onChange={(event) =>
                            updatePeriod({ [key]: event.currentTarget.value })
                          }
                        />
                      )}
                      {issue && (
                        <p
                          id={`${fieldId}-error`}
                          className='text-destructive text-sm'
                          role='alert'
                        >
                          {t(issue.message)}
                        </p>
                      )}
                    </div>
                  )
                })}
              </div>
              {rowIssues.length === 0 && (
                <ModelScheduleLimitPreview
                  defaults={props.rule.defaults}
                  multiplier={period.multiplier}
                />
              )}
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <div className='min-w-0 text-sm'>
                  {crossesMidnight && (
                    <span className='text-muted-foreground'>
                      {t('Crosses midnight')}
                    </span>
                  )}
                  {rowIssues.some((issue) => issue.path.length === 3) && (
                    <p className='text-destructive' role='alert'>
                      {t('This time period overlaps another period.')}
                    </p>
                  )}
                </div>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  aria-label={t('Remove time period {{number}}', {
                    number: row + 1,
                  })}
                  onClick={() => {
                    setRowKeys(
                      rows
                        .filter(({ key }) => key !== rowKey)
                        .map(({ key }) => key)
                    )
                    update({
                      ...schedule,
                      periods: schedule.periods.filter(
                        (_, index) => index !== row
                      ),
                    })
                  }}
                >
                  {t('Remove')}
                </Button>
              </div>
            </fieldset>
          )
        })}
    </div>
  )
}
