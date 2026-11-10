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
import { z } from 'zod'

const limitSchema = z
  .object({
    enabled: z.boolean(),
    limit: z.number().int().min(0).max(2147483647),
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
const modelRuleSchema = z
  .object({
    defaults: limitsSchema.default(unlimitedResourceLimits),
    users: z
      .record(z.string().regex(/^[1-9]\d*$/), limitsSchema.partial())
      .default({}),
  })
  .strict()
const exactModelSchema = z
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
