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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { UserModelResourceTable } from '../resource-usage-table'
import { ResourceUsageMeter } from '../resource-usage-values'
import type { UserModelResource } from '../types'

const row: UserModelResource = {
  user_id: 42,
  model: `exact/${'long-name'.repeat(30)}`,
  configured: true,
  available: false,
  limits: {
    concurrency: { enabled: true, limit: 0 },
    rpm: { enabled: false, limit: 0 },
    input_tpm: { enabled: true, limit: 1000 },
  },
  sources: {
    concurrency: 'user_override',
    rpm: 'model_default',
    input_tpm: 'model_default',
  },
}
it('shows unknown usage with blocked and unlimited limits and their sources', () => {
  render(
    <UserModelResourceTable
      rows={[row]}
      total={1}
      page={1}
      pageSize={20}
      onPageSizeChange={vi.fn()}
      onPageChange={vi.fn()}
      showUser
      showSources
    />
  )
  const cells = within(screen.getAllByRole('row')[1])
  expect(cells.getByText('42')).toBeVisible()
  expect(cells.getByText(row.model)).toBeVisible()
  expect(screen.getByRole('table')).toHaveClass('min-w-[40rem]', 'table-fixed')
  expect(cells.getByText('Unknown / Blocked')).toBeVisible()
  expect(cells.getByText('Unknown / Unlimited')).toBeVisible()
  expect(cells.getByText('User override')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()
})
it('uses the server total for pagination and requests the next page', async () => {
  const change = vi.fn()
  render(
    <UserModelResourceTable
      rows={[
        {
          ...row,
          available: true,
          usage: { concurrency: 0, rpm: 2, input_tpm: 15 },
        },
      ]}
      total={41}
      page={1}
      pageSize={20}
      onPageSizeChange={vi.fn()}
      onPageChange={change}
    />
  )
  expect(
    within(screen.getByRole('navigation', { name: 'Page' })).getByText('41')
  ).toBeVisible()
  expect(screen.getByText('1 / 3')).toBeVisible()
  expect(screen.getByText('2 / Unlimited')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Go to next page' }))
  expect(change).toHaveBeenCalledWith(2)
})
it.each([
  [39, 'Normal'],
  [40, 'Elevated usage'],
  [79, 'Elevated usage'],
  [80, 'Near or at limit'],
  [120, 'Near or at limit'],
] as const)(
  'shows model utilization %i%% with the total-resource status %s',
  (usage, status) => {
    render(
      <UserModelResourceTable
        rows={[
          {
            ...row,
            available: true,
            limits: {
              ...row.limits,
              concurrency: { enabled: true, limit: 100 },
            },
            usage: { concurrency: usage, rpm: 2, input_tpm: 15 },
          },
        ]}
        total={1}
        page={1}
        pageSize={20}
        onPageSizeChange={vi.fn()}
        onPageChange={vi.fn()}
        showUtilization
      />
    )
    expect(
      screen.getByText(`${usage} / 100 · ${status} · ${usage}%`)
    ).toBeVisible()
    expect(
      screen.getByRole('progressbar', { name: 'Concurrency' })
    ).toHaveAttribute('aria-valuenow', String(Math.min(usage, 100)))
    expect(screen.getByText('2 / Unlimited')).toBeVisible()
    expect(
      screen.queryByRole('progressbar', { name: 'RPM' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('User override')).not.toBeInTheDocument()
  }
)
it('shows an explicit empty state when no model resources exist', () => {
  render(
    <UserModelResourceTable
      rows={[]}
      total={0}
      page={1}
      pageSize={20}
      onPageSizeChange={vi.fn()}
      onPageChange={vi.fn()}
    />
  )
  expect(screen.getByText('No data available')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()
})
it('shows a zero limit as blocked with a full danger indicator', () => {
  const view = render(
    <ResourceUsageMeter
      snapshot={{
        ...row,
        available: true,
        usage: { concurrency: 0, rpm: 0, input_tpm: 0 },
      }}
      resource='concurrency'
      label='Concurrency'
    />
  )
  expect(screen.getByText('0 / 0 · Blocked · 100%')).toBeVisible()
  expect(
    screen.getByRole('progressbar', { name: 'Concurrency' })
  ).toHaveAttribute('aria-valuenow', '100')
  expect(view.container.firstElementChild).toHaveClass('text-destructive')
})
