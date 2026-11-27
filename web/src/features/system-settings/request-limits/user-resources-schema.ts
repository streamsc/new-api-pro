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
import Decimal from 'decimal.js'
import { z } from 'zod'

const maxResourceLimit = 2147483647
const ResourceDecimal = Decimal.clone({ precision: 32 })
const limitSchema = z
  .object({
    enabled: z.boolean(),
    limit: z.number().int().min(0).max(maxResourceLimit),
  })
  .strict()
const limitsSchema = z
  .object({
    concurrency: limitSchema,
    input_tpm: limitSchema,
    rpm: limitSchema,
  })
  .strict()
export const unlimitedResourceLimits = {
  concurrency: { enabled: false, limit: 0 },
  input_tpm: { enabled: false, limit: 0 },
  rpm: { enabled: false, limit: 0 },
}
const schedulePeriodSchema = z
  .object({ start: z.string(), end: z.string(), multiplier: z.number() })
  .strict()
const scheduleSchema = z
  .object({
    timezone: z.string(),
    periods: z.array(schedulePeriodSchema).min(1),
  })
  .strict()

export type ResourceSchedule = z.infer<typeof scheduleSchema>
export type ResourceSchedulePeriod = z.infer<typeof schedulePeriodSchema>
export const minutesPerDay = 24 * 60

export function resourceScheduleMinute(value: string): number | undefined {
  if (!/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)) return undefined
  const [hour, minute] = value.split(':').map(Number)
  return hour * 60 + minute
}

export function resourceScheduleSpans(
  start: number,
  end: number
): [number, number][] {
  if (start < end) return [[start, end]]
  if (start === end) return []
  const spans: [number, number][] = [[start, minutesPerDay]]
  if (end > 0) spans.push([0, end])
  return spans
}

export function scaleModelResourceLimit(
  limit: number,
  multiplier: number
): number {
  if (limit === 0) return 0
  return Math.max(
    1,
    new ResourceDecimal(limit).times(multiplier).floor().toNumber()
  )
}

export const modelRuleSchema = z
  .object({
    defaults: limitsSchema.default(unlimitedResourceLimits),
    users: z
      .record(z.string().regex(/^[1-9]\d*$/), limitsSchema.partial())
      .default({}),
    schedule: scheduleSchema.optional(),
  })
  .strict()
  .superRefine((rule, ctx) => {
    const { schedule } = rule
    if (!schedule) return
    try {
      if (!/^[a-z]/i.test(schedule.timezone)) throw new RangeError()
      new Intl.DateTimeFormat('en', { timeZone: schedule.timezone })
    } catch {
      ctx.addIssue({
        code: 'custom',
        path: ['schedule', 'timezone'],
        message: 'Choose a valid IANA timezone.',
      })
    }
    let maxLimit = 0
    for (const limits of [rule.defaults, ...Object.values(rule.users)]) {
      for (const limit of Object.values(limits)) {
        if (limit?.enabled) maxLimit = Math.max(maxLimit, limit.limit)
      }
    }
    const spans: { start: number; end: number; row: number }[] = []
    const conflictingRows = new Set<number>()
    schedule.periods.forEach((period, row) => {
      const path = ['schedule', 'periods', row]
      const start = resourceScheduleMinute(period.start)
      const end = resourceScheduleMinute(period.end)
      if (start === undefined) {
        ctx.addIssue({
          code: 'custom',
          path: [...path, 'start'],
          message: 'Enter a valid time.',
        })
      }
      if (end === undefined) {
        ctx.addIssue({
          code: 'custom',
          path: [...path, 'end'],
          message: 'Enter a valid time.',
        })
      }
      if (start !== undefined && end !== undefined) {
        if (start === end) {
          ctx.addIssue({
            code: 'custom',
            path: [...path, 'end'],
            message: 'Start and end times must differ.',
          })
        }
        for (const [from, to] of resourceScheduleSpans(start, end)) {
          spans.push({ start: from, end: to, row })
        }
      }
      if (period.multiplier <= 0) {
        ctx.addIssue({
          code: 'custom',
          path: [...path, 'multiplier'],
          message: 'Enter a positive multiplier.',
        })
      } else if (
        scaleModelResourceLimit(maxLimit, period.multiplier) > maxResourceLimit
      ) {
        ctx.addIssue({
          code: 'custom',
          path: [...path, 'multiplier'],
          message: 'The multiplier exceeds the maximum limit.',
        })
      }
    })
    spans.sort((a, b) => a.start - b.start)
    let previous: (typeof spans)[number] | undefined
    for (const span of spans) {
      if (previous && span.start < previous.end) {
        conflictingRows.add(previous.row)
        conflictingRows.add(span.row)
      }
      if (!previous || span.end > previous.end) previous = span
    }
    for (const row of conflictingRows) {
      ctx.addIssue({
        code: 'custom',
        path: ['schedule', 'periods', row],
        message: 'This time period overlaps another period.',
      })
    }
  })
export type ModelResourceRule = z.infer<typeof modelRuleSchema>
export const exactModelSchema = z
  .string()
  .min(1)
  .refine(
    (name) =>
      new TextEncoder().encode(name).length <= 512 &&
      name.trim() === name &&
      !/\p{Cc}/u.test(name)
  )
export const userResourcesConfigSchema = z
  .object({
    // Zod records skip __proto__; a Map validates every exact model name.
    models: z
      .preprocess(
        (value) =>
          value && typeof value === 'object' && !Array.isArray(value)
            ? new Map(Object.entries(value))
            : value,
        z.map(exactModelSchema, modelRuleSchema)
      )
      .transform((models) => Object.fromEntries(models))
      .optional(),
    defaults: limitsSchema,
    users: z.record(z.string().regex(/^[1-9]\d*$/), limitsSchema.partial()),
  })
  .strict()

export type UserResourcesConfig = z.infer<typeof userResourcesConfigSchema>
