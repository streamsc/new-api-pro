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
import {
  QueryClient,
  QueryClientProvider,
  focusManager,
} from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import type { PropsWithChildren } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import type {
  Channel,
  GetChannelsResponse,
  SearchChannelsParams,
} from '../../types'
import { useChannelListQuery } from '../use-channel-list-query'

function response(count: number | null = 3, id = 1): GetChannelsResponse {
  return {
    success: true,
    data: {
      items: [
        {
          id,
          name: `channel-${id}`,
          in_flight: count,
          setting: '{"max_concurrency":10}',
        } as Channel,
      ],
      total: 1,
      page: 1,
      page_size: 10,
      in_flight_scope: 'process',
      in_flight_available: true,
    },
  }
}

let client: QueryClient
beforeEach(() => {
  vi.useFakeTimers()
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    value: 'visible',
  })
  focusManager.setFocused(undefined)
  client = new QueryClient({
    defaultOptions: { queries: { retry: 3, staleTime: 10_000 } },
  })
})
afterEach(() => {
  client.clear()
  vi.useRealTimers()
  vi.restoreAllMocks()
  focusManager.setFocused(undefined)
})

function mount(params: SearchChannelsParams = {}) {
  return renderHook((next: SearchChannelsParams) => useChannelListQuery(next), {
    initialProps: params,
    wrapper: (props: PropsWithChildren) => (
      <QueryClientProvider client={client}>
        {props.children}
      </QueryClientProvider>
    ),
  })
}

async function tick(ms = 1) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

async function visibility(state: 'visible' | 'hidden') {
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: state,
    })
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true }))
  })
  await tick()
}

describe('channel list query lifecycle', () => {
  test('loads immediately, refreshes every minute, pauses when hidden and stops on unmount', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: response() })
    const view = mount()
    await tick()
    expect(get).toHaveBeenCalledTimes(1)
    await tick(59_998)
    expect(get).toHaveBeenCalledTimes(1)
    await tick(2)
    expect(get).toHaveBeenCalledTimes(2)
    await visibility('hidden')
    await tick(120_000)
    expect(get).toHaveBeenCalledTimes(2)
    await visibility('visible')
    expect(get).toHaveBeenCalledTimes(3)
    await tick(60_000)
    expect(get).toHaveBeenCalledTimes(4)
    view.unmount()
    await tick(120_000)
    expect(get).toHaveBeenCalledTimes(4)
  })

  test('manual refresh and slow requests share one request without queued ticks', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: response() })
    const view = mount()
    await tick()
    const pending = Promise.withResolvers<{ data: GetChannelsResponse }>()
    get.mockReturnValueOnce(pending.promise)
    act(() => {
      void view.result.current.refetch({ cancelRefetch: false })
    })
    await tick(180_000)
    act(() => {
      void view.result.current.refetch({ cancelRefetch: false })
    })
    expect(get).toHaveBeenCalledTimes(2)
    await act(async () => {
      pending.resolve({ data: response(0) })
    })
    await tick()
    expect(view.result.current.channels[0].in_flight).toBe(0)
    expect(get).toHaveBeenCalledTimes(2)
    await tick(60_000)
    expect(get).toHaveBeenCalledTimes(3)
  })

  test.each(['http', 'business'])(
    'keeps configuration but clears counts after %s failure, without retry',
    async (failure) => {
      const get = vi.spyOn(api, 'get').mockResolvedValue({ data: response() })
      const view = mount()
      await tick()
      if (failure === 'http') {
        get.mockRejectedValueOnce(new Error('offline'))
      } else {
        get.mockResolvedValueOnce({
          data: { success: false, message: 'unavailable' },
        })
      }
      await act(async () => {
        await view.result.current.refetch({ cancelRefetch: false })
      })
      await tick()
      expect(view.result.current.channels[0]).toMatchObject({
        name: 'channel-1',
        setting: '{"max_concurrency":10}',
        in_flight: null,
      })
      expect(view.result.current.concurrencyUnavailable).toBe(true)
      expect(get).toHaveBeenLastCalledWith(
        '/api/channel',
        expect.objectContaining({
          skipErrorHandler: true,
          skipBusinessError: true,
        })
      )
      await tick(30_000)
      expect(get).toHaveBeenCalledTimes(2)
      get.mockResolvedValueOnce({ data: response(0) })
      await tick(30_000)
      expect(view.result.current.channels[0].in_flight).toBe(0)
      expect(view.result.current.concurrencyUnavailable).toBe(false)
    }
  )

  test('first-load failure does not invent configuration', async () => {
    vi.spyOn(api, 'get').mockRejectedValue(new Error('offline'))
    const view = mount()
    await tick()
    expect(view.result.current.isError).toBe(true)
    expect(view.result.current.data).toBeUndefined()
    expect(view.result.current.channels).toEqual([])
  })

  test('treats missing counts and an unavailable empty batch as unknown', async () => {
    const missing = response()
    if (!missing.data) throw new Error('Fixture has no channel data')
    delete missing.data.items[0].in_flight
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: missing })
    const view = mount()
    await tick()
    expect(view.result.current.channels[0].in_flight).toBeNull()
    expect(view.result.current.concurrencyUnavailable).toBe(true)
    get.mockResolvedValueOnce({
      data: {
        ...missing,
        data: { ...missing.data, items: [], in_flight_available: false },
      },
    })
    await act(async () => {
      await view.result.current.refetch({ cancelRefetch: false })
    })
    await tick()
    expect(view.result.current.channels).toEqual([])
    expect(view.result.current.concurrencyUnavailable).toBe(true)
  })

  test('page placeholders are unknown and late search results cannot overwrite the current page', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: response() })
    const view = mount({ p: 1 })
    await tick()
    const oldSearch = Promise.withResolvers<{ data: GetChannelsResponse }>()
    get.mockReturnValueOnce(oldSearch.promise)
    view.rerender({ keyword: 'old', p: 1 })
    await tick()
    expect(view.result.current.isPlaceholderData).toBe(true)
    expect(view.result.current.channels[0].in_flight).toBeNull()
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/search',
      expect.objectContaining({ params: { keyword: 'old', p: 1 } })
    )
    get.mockResolvedValueOnce({ data: response(2, 2) })
    view.rerender({ keyword: 'new', p: 2 })
    await tick()
    await act(async () => {
      oldSearch.resolve({ data: response(9, 9) })
    })
    await tick()
    expect(view.result.current.channels[0]).toMatchObject({
      id: 2,
      in_flight: 2,
    })
  })
})
