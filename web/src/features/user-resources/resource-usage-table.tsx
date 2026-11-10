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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { DataTablePagination, StaticDataTable } from '@/components/data-table'

import {
  ResourceUsageMeter,
  ResourceUsageValues,
} from './resource-usage-values'
import type { UserModelResource } from './types'

export function UserModelResourceTable(props: {
  rows: UserModelResource[]
  total: number
  page: number
  pageSize: number
  onPageSizeChange: (size: number) => void
  onPageChange: (page: number) => void
  showUser?: boolean
  showSources?: boolean
  showUtilization?: boolean
}) {
  const { t } = useTranslation()
  const table = useReactTable({
    data: props.rows,
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    rowCount: props.total,
    pageCount: Math.ceil(props.total / props.pageSize),
    state: {
      pagination: { pageIndex: props.page - 1, pageSize: props.pageSize },
    },
    onPaginationChange: (updater) => {
      const current = { pageIndex: props.page - 1, pageSize: props.pageSize }
      const next = typeof updater === 'function' ? updater(current) : updater
      if (next.pageSize !== props.pageSize) {
        props.onPageSizeChange(next.pageSize)
        props.onPageChange(1)
      } else props.onPageChange(next.pageIndex + 1)
    },
  })
  return (
    <div className='min-w-0 space-y-3'>
      <StaticDataTable
        data={props.rows}
        getRowKey={(row) => `${row.user_id}:${row.model}`}
        emptyContent={
          props.showSources
            ? t('No model activity or overrides.')
            : t('No data available')
        }
        className='overflow-x-auto'
        tableClassName='min-w-[40rem] table-fixed'
        columns={[
          ...(props.showUser
            ? [
                {
                  id: 'user',
                  header: t('User ID'),
                  className: 'w-20',
                  cell: (row: UserModelResource) => String(row.user_id),
                },
              ]
            : []),
          {
            id: 'model',
            header: t('Model'),
            className: 'w-48',
            cell: (row) => row.model,
          },
          ...(['concurrency', 'rpm', 'input_tpm'] as const).map((resource) => ({
            id: resource,
            className: 'whitespace-normal',
            cellClassName: 'whitespace-normal',
            header: t(
              {
                concurrency: 'Concurrency',
                rpm: 'RPM',
                input_tpm: 'Estimated input TPM',
              }[resource]
            ),
            cell: (row: UserModelResource) =>
              props.showUtilization ? (
                <ResourceUsageMeter
                  snapshot={row}
                  resource={resource}
                  label={t(
                    {
                      concurrency: 'Concurrency',
                      rpm: 'RPM',
                      input_tpm: 'Estimated input TPM',
                    }[resource]
                  )}
                />
              ) : (
                <ResourceUsageValues
                  snapshot={row}
                  resource={resource}
                  showSources={props.showSources}
                />
              ),
          })),
        ]}
      />
      <DataTablePagination table={table} compact />
    </div>
  )
}
