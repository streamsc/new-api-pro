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
import type { ColumnDef } from '@tanstack/react-table'
import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { expect, test } from 'vitest'

import { useDataTable } from '../../hooks/use-data-table'
import { DataTableRow } from '../data-table-row'

type Item = { id: string; count: number; children?: Item[] }
const columns: ColumnDef<Item>[] = [
  {
    id: 'name',
    cell: ({ row }) => (
      <>
        {row.original.id}
        {row.getCanExpand() && (
          <button
            type='button'
            aria-label='Toggle children'
            aria-expanded={row.getIsExpanded()}
            onClick={row.getToggleExpandedHandler()}
          >
            {row.getIsExpanded() ? 'Collapse' : 'Expand'}
          </button>
        )}
        <input
          aria-label={`Select ${row.id}`}
          type='checkbox'
          checked={row.getIsSelected()}
          onChange={row.getToggleSelectedHandler()}
        />
      </>
    ),
  },
  { accessorKey: 'count', cell: ({ row }) => row.original.count },
]

function TableFixture() {
  const [data, setData] = useState<Item[]>([
    { id: 'tag', count: 0, children: [{ id: 'child', count: 3 }] },
  ])
  const { table } = useDataTable({
    data,
    columns,
    getRowId: (row: Item) => row.id,
    getSubRows: (row: Item) => row.children,
    withExpandedRowModel: true,
    autoResetExpanded: false,
  })
  return (
    <>
      <button
        type='button'
        onClick={() =>
          setData([
            { id: 'tag', count: 0, children: [{ id: 'child', count: 4 }] },
          ])
        }
      >
        Refresh
      </button>
      <table>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <DataTableRow key={row.id} row={row} cellRenderColumns={columns} />
          ))}
        </tbody>
      </table>
    </>
  )
}

test('expansion rendering and selection survive a data refresh', () => {
  render(<TableFixture />)
  fireEvent.click(screen.getByRole('button', { name: 'Toggle children' }))
  expect(
    screen.getByRole('button', { name: 'Toggle children' })
  ).toHaveAttribute('aria-expanded', 'true')
  expect(
    screen.getByRole('button', { name: 'Toggle children' })
  ).toHaveTextContent('Collapse')
  fireEvent.click(screen.getByRole('checkbox', { name: 'Select child' }))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(screen.getByRole('checkbox', { name: 'Select child' })).toBeChecked()
  expect(
    screen.getByRole('button', { name: 'Toggle children' })
  ).toHaveTextContent('Collapse')
  expect(screen.getByRole('cell', { name: '4' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Toggle children' }))
  expect(
    screen.queryByRole('checkbox', { name: 'Select child' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Toggle children' })
  ).toHaveTextContent('Expand')
})
