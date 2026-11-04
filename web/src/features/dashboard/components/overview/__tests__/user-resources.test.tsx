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
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { UserResourcesCard, type UserResources } from '../user-resources-card'

let client: QueryClient
const sample = (usage: number, enabled = true): UserResources => ({
  user_id: 12,
  username: 'alice',
  available: true,
  updated_at: 1793700000,
  mode: 'redis',
  limits: {
    concurrency: { enabled, limit: 100 },
    input_tpm: { enabled: false, limit: 0 },
    rpm: { enabled: false, limit: 0 },
  },
  usage: { concurrency: usage, input_tpm: 30, rpm: 1 },
})
function mount() {
  return render(
    <QueryClientProvider client={client}>
      <UserResourcesCard />
    </QueryClientProvider>
  )
}
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  focusManager.setFocused(true)
})
afterEach(() => {
  cleanup()
  client.clear()
  focusManager.setFocused(undefined)
  vi.useRealTimers()
})
describe('User resources card', () => {
  it.each([
    [39, 'Normal'],
    [40, 'Elevated usage'],
    [79, 'Elevated usage'],
    [80, 'Near or at limit'],
  ] as const)(
    'shows %i percent with its text threshold',
    async (amount, status) => {
      vi.spyOn(api, 'get').mockResolvedValue({
        data: { success: true, data: sample(amount) },
      })
      mount()
      expect(await screen.findByText(new RegExp(status))).toBeInTheDocument()
      expect(
        screen.getByRole('progressbar', { name: 'Concurrency' })
      ).toHaveAttribute('aria-valuenow', String(amount))
      expect(screen.getByText(/alice/)).toBeInTheDocument()
      expect(
        screen.getByText('Input TPM (estimated, last 60 seconds)')
      ).toBeInTheDocument()
    }
  )
  it('shows unlimited usage without a percentage or progress bar', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: sample(12, false) },
    })
    mount()
    expect(await screen.findByText(/12 \/ Unlimited/)).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  })
  it.each([false, true])(
    'does not show zero or stale usage when a read fails (%s)',
    async (networkFailure) => {
      const mock = vi
        .spyOn(api, 'get')
        .mockResolvedValue({ data: { success: true, data: sample(39) } })
      mount()
      await screen.findByRole('progressbar')
      if (networkFailure) mock.mockRejectedValue(new Error('offline'))
      else {
        mock.mockResolvedValue({
          data: {
            success: true,
            data: { ...sample(0), available: false, usage: undefined },
          },
        })
      }
      await act(async () => {
        await client.invalidateQueries({ queryKey: ['self-resources'] })
      })
      expect(
        await screen.findByText('Resource usage temporarily unavailable')
      ).toBeInTheDocument()
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
    }
  )
  it('pauses interval requests when hidden and stops after unmount', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: sample(39) } })
    const view = mount()
    await screen.findByRole('progressbar')
    vi.useFakeTimers()
    focusManager.setFocused(false)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000)
    })
    expect(get).toHaveBeenCalledTimes(1)
    await act(async () => {
      focusManager.setFocused(true)
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(get.mock.calls.length).toBeGreaterThan(1)
    view.unmount()
    const calls = get.mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000)
    })
    expect(get).toHaveBeenCalledTimes(calls)
  })
})
