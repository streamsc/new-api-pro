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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { UserResourcesSection } from '../user-resources-section'

let client: QueryClient
let actions: HTMLDivElement
const config = {
  defaults: {
    concurrency: { enabled: true, limit: 0 },
    input_tpm: { enabled: false, limit: 0 },
    rpm: { enabled: true, limit: 120 },
  },
  users: { '42': { rpm: { enabled: false, limit: 0 } } },
}
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  actions = document.createElement('div')
  document.body.append(actions)
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: config },
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  actions.remove()
})
function mount() {
  render(
    <QueryClientProvider client={client}>
      <SettingsPageProvider actionsContainer={actions}>
        <UserResourcesSection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}
it('requires a reason and saves explicit zero and unlimited overrides', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  await screen.findByRole('textbox', { name: 'Defaults and user overrides' })
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(put).not.toHaveBeenCalled()
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Capacity review'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/option/user_resources', {
      config,
      reason: 'Capacity review',
    })
  )
})
it('rejects negative resource limits without sending a write', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  const editor = await screen.findByRole('textbox', {
    name: 'Defaults and user overrides',
  })
  fireEvent.input(editor, {
    target: {
      value: JSON.stringify({
        ...config,
        defaults: { ...config.defaults, rpm: { enabled: true, limit: -1 } },
      }),
    },
  })
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Review'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText('Invalid resource limits configuration')
  ).toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
})
it('retains the change reason and edited config after a failed save', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Store unavailable' },
  })
  const user = userEvent.setup()
  mount()
  const editor = await screen.findByRole('textbox', {
    name: 'Defaults and user overrides',
  })
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Keep this reason'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  expect(
    screen.getByRole('textbox', { name: 'Reason for change' })
  ).toHaveValue('Keep this reason')
  expect(editor).toHaveValue(JSON.stringify(config, null, 2))
})
