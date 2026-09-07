/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, test } from 'vitest'

import type { Channel } from '../../types'
import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
} from '../channel-form'
import { aggregateChannelsByTag, getChannelConcurrency } from '../channel-utils'

function channelFixture(
  id: number,
  inFlight: number,
  maximum: number
): Channel {
  return {
    id,
    tag: 'production',
    setting: JSON.stringify({ max_concurrency: maximum }),
    in_flight: inFlight,
    used_quota: 0,
    response_time: 0,
    priority: 0,
    weight: 0,
    group: 'default',
    status: 1,
  } as Channel
}

describe('channel concurrency settings', () => {
  test('persists positive limits and omits the unlimited default', () => {
    const limited = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        max_concurrency: 8,
      })
    )
    const unlimited = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        max_concurrency: 0,
      })
    )

    expect(limited.max_concurrency).toBe(8)
    expect(unlimited).not.toHaveProperty('max_concurrency')
  })

  test('rejects negative and fractional limits', () => {
    expect(
      channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        max_concurrency: -1,
      }).success
    ).toBe(false)
    expect(
      channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        max_concurrency: 1.5,
      }).success
    ).toBe(false)
  })

  test('restores the configured limit when editing a channel', () => {
    const form = transformChannelToFormDefaults({
      type: 1,
      status: 1,
      setting: JSON.stringify({ max_concurrency: 12 }),
      settings: '{}',
      channel_info: { multi_key_mode: 'random' },
    } as Channel)

    expect(form.max_concurrency).toBe(12)
  })
})

describe('channel concurrency display', () => {
  test('does not infer a shared pool for finite tag children', () => {
    const [row] = aggregateChannelsByTag([
      channelFixture(1, 2, 4),
      channelFixture(2, 3, 6),
    ])

    expect(getChannelConcurrency(row)).toBeNull()
    expect(row.in_flight).toBeNull()
  })

  test('does not infer a shared pool for mixed and disabled tag children', () => {
    const [row] = aggregateChannelsByTag([
      channelFixture(1, 2, 4),
      { ...channelFixture(2, 1, 0), status: 0 },
    ])

    expect(getChannelConcurrency(row)).toBeNull()
  })

  test.each([0, 3, null, undefined])(
    'preserves known and unknown counts: %s',
    (count) => {
      for (const maximum of [0, 10]) {
        expect(
          getChannelConcurrency({
            ...channelFixture(1, 0, maximum),
            in_flight: count,
          })
        ).toEqual({
          inFlight: count ?? null,
          maximum,
        })
      }
    }
  )
})
