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
import { flexRender } from '@tanstack/react-table'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { DataTableViewOptions, useDataTable } from '@/components/data-table'

import { DEFAULT_COMMON_LOG_COLUMN_VISIBILITY } from '../../constants'
import type { UsageLog } from '../../data/schema'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { UserAgentCell } from '../user-agent-cell'

const storageKey = 'usage-logs:common:self:column-visibility'
const logs: UsageLog[] = [
  {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: '',
    token_name: '',
    model_name: 'test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 0,
    channel_name: '',
    token_id: 0,
    group: '',
    ip: '',
    request_id: '',
    upstream_request_id: '',
    other: JSON.stringify({ user_agent: 'Client/1.0' }),
  },
]

function ColumnPicker() {
  const columns = useCommonLogsColumns(false, false)
  const { table } = useDataTable({
    data: logs,
    columns,
    initialColumnVisibility: DEFAULT_COMMON_LOG_COLUMN_VISIBILITY,
    columnVisibilityStorageKey: storageKey,
  })
  return (
    <>
      <DataTableViewOptions table={table} />
      {table
        .getRowModel()
        .rows[0].getVisibleCells()
        .filter((cell) => cell.column.id === 'user_agent')
        .map((cell) => (
          <div key={cell.id}>
            {flexRender(cell.column.columnDef.cell, cell.getContext())}
          </div>
        ))}
    </>
  )
}

beforeEach(() => {
  vi.stubGlobal('localStorage', window.localStorage)
  window.localStorage.clear()
})
afterEach(() => {
  window.localStorage.clear()
  vi.unstubAllGlobals()
})

test('UA is hidden initially, selectable and persists through hide/show and remount', async () => {
  const user = userEvent.setup()
  let view = render(<ColumnPicker />)
  expect(
    screen.queryByRole('button', { name: 'User Agent' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'View' }))
  expect(screen.getByRole('menuitemcheckbox', { name: 'UA' })).toHaveAttribute(
    'aria-checked',
    'false'
  )
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'UA' }))
  await user.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'User Agent' })).toBeVisible()
  view.unmount()
  view = render(<ColumnPicker />)
  expect(screen.getByRole('button', { name: 'User Agent' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'View' }))
  expect(screen.getByRole('menuitemcheckbox', { name: 'UA' })).toHaveAttribute(
    'aria-checked',
    'true'
  )
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'UA' }))
  view.unmount()
  render(<ColumnPicker />)
  expect(
    screen.queryByRole('button', { name: 'User Agent' })
  ).not.toBeInTheDocument()
})

test('an older saved preference keeps UA hidden without losing other columns', async () => {
  window.localStorage.setItem(storageKey, JSON.stringify({ model_name: false }))
  const user = userEvent.setup()
  render(<ColumnPicker />)
  await user.click(screen.getByRole('button', { name: 'View' }))
  expect(screen.getByRole('menuitemcheckbox', { name: 'UA' })).toHaveAttribute(
    'aria-checked',
    'false'
  )
  expect(
    screen.getByRole('menuitemcheckbox', { name: 'Model' })
  ).toHaveAttribute('aria-checked', 'false')
})

test.each([undefined, ''])(
  'missing or empty UA renders a dash without a copy action (%s)',
  (value) => {
    render(<UserAgentCell value={value} />)
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  }
)

test('long UA is truncated in the cell and opens as plain text with keyboard and copies in full', async () => {
  const user = userEvent.setup()
  const value = `<script>alert(1)</script>${' Client/1.0'.repeat(90)}`
  render(<UserAgentCell value={value} />)
  expect(screen.getByText(value)).toHaveClass('truncate')
  await user.tab()
  expect(screen.getByRole('button', { name: 'User Agent' })).toHaveFocus()
  await user.keyboard('{Enter}')
  const dialog = await screen.findByRole('dialog', { name: 'User Agent' })
  expect(within(dialog).getByText(value)).toHaveClass(
    'break-all',
    'whitespace-pre-wrap'
  )
  expect(dialog.querySelector('script')).toBeNull()
  await user.click(
    within(dialog).getByRole('button', { name: 'Copy to clipboard' })
  )
  expect(await navigator.clipboard.readText()).toBe(value)
  expect(within(dialog).getByRole('button', { name: 'Copied' })).toBeVisible()
  await user.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'User Agent' })).toHaveFocus()
})
