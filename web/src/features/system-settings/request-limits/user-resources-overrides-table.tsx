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
  getCoreRowModel,
  getPaginationRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTablePagination,
  StaticDataTable,
  StaticRowActions,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { UserResourcesOverrideDrawer } from './user-resources-override-drawer'
import type { UserResourcesConfig } from './user-resources-schema'
import {
  UserResourcesUserPicker,
  UserResourceUserLabel,
} from './user-resources-user-picker'

export function UserResourcesOverridesTable(props: {
  config: UserResourcesConfig
  onChange: (config: UserResourcesConfig) => void
}) {
  const { t, i18n } = useTranslation()
  const id = useId()
  const [filter, setFilter] = useState('')
  const [editing, setEditing] = useState<{ id: string | null } | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)
  const data = useMemo(
    () =>
      Object.keys(props.config.users).filter(
        (user) => !filter || user === filter
      ),
    [props.config.users, filter]
  )
  const table = useReactTable({
    data,
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    initialState: { pagination: { pageIndex: 0, pageSize: 20 } },
  })
  const labels = {
    concurrency: t('Concurrency'),
    input_tpm: t('Estimated input TPM'),
    rpm: t('RPM'),
  }
  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-end gap-2'>
        <div className='min-w-0 flex-1 space-y-2'>
          <Label htmlFor={id}>{t('Find user override')}</Label>
          <UserResourcesUserPicker
            id={id}
            value={filter}
            onChange={(value) => {
              setFilter(value)
              table.setPageIndex(0)
            }}
          />
        </div>
        {filter && (
          <Button
            type='button'
            variant='outline'
            onClick={() => {
              setFilter('')
              table.setPageIndex(0)
            }}
          >
            {t('Clear')}
          </Button>
        )}
        <Button type='button' onClick={() => setEditing({ id: null })}>
          {t('Add user override')}
        </Button>
      </div>
      <StaticDataTable
        className='max-h-112 w-full min-w-0 overflow-auto'
        containerProps={{
          tabIndex: 0,
          role: 'region',
          'aria-label': t('Defaults and user overrides'),
        }}
        tableProps={{ withContainer: false }}
        tableClassName='min-w-160 [&_thead]:bg-background [&_thead]:sticky [&_thead]:top-0 [&_thead]:z-10 [&_tbody>tr]:h-11'
        data={table.getRowModel().rows.map((row) => row.original)}
        getRowKey={(user) => user}
        emptyContent={t(
          'No user overrides found. Other users inherit the default limits.'
        )}
        columns={[
          {
            id: 'user',
            header: t('Username'),
            cell: (user) => <UserResourceUserLabel id={user} />,
          },
          ...(['concurrency', 'input_tpm', 'rpm'] as const).map((resource) => ({
            id: resource,
            header: labels[resource],
            cell: (user: string) => {
              const override = props.config.users[user][resource]
              const limit = override ?? props.config.defaults[resource]
              const value = limit.enabled
                ? formatNumber(
                    limit.limit,
                    toIntlLocale(i18n.resolvedLanguage || i18n.language)
                  )
                : t('Unlimited')
              return override ? value : `${t('Inherit default')} (${value})`
            },
          })),
          {
            id: 'actions',
            header: t('Actions'),
            cell: (user) => (
              <StaticRowActions
                editLabel={`${t('Edit')} ${user}`}
                deleteLabel={t('Remove override')}
                menuLabel={`${t('Actions')} ${user}`}
                onEdit={() => setEditing({ id: user })}
                onDelete={() => setRemoving(user)}
              />
            ),
          },
        ]}
      />
      <DataTablePagination table={table} compact />
      {editing && (
        <UserResourcesOverrideDrawer
          config={props.config}
          userId={editing.id}
          onClose={() => setEditing(null)}
          onApply={(user, limits) => {
            props.onChange({
              ...props.config,
              users: { ...props.config.users, [user]: limits },
            })
            setEditing(null)
          }}
        />
      )}
      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) setRemoving(null)
        }}
        title={t('Remove override')}
        desc={t('This user will inherit the default limits after saving.')}
        confirmText={t('Remove override')}
        handleConfirm={() => {
          if (!removing) return
          const users = { ...props.config.users }
          delete users[removing]
          props.onChange({ ...props.config, users })
          table.setPageIndex(0)
          setRemoving(null)
        }}
      />
    </div>
  )
}
