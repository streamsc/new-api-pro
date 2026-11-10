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
import { useQuery } from '@tanstack/react-query'
import {
  getCoreRowModel,
  getPaginationRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable, DataTablePagination } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { ResourceLimitFields } from './user-resources-limit-fields'
import { UserResourcesOverridesTable } from './user-resources-overrides-table'
import {
  unlimitedResourceLimits,
  userResourcesConfigSchema,
  type UserResourcesConfig,
} from './user-resources-schema'

export function UserResourcesModelEditor(props: {
  config: UserResourcesConfig
  onChange: (config: UserResourcesConfig) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [selected, setSelected] = useState('')
  const [removing, setRemoving] = useState(false)
  const [error, setError] = useState('')
  const models = props.config.models ?? {}
  const names = useMemo(
    () => Object.keys(props.config.models ?? {}).sort(),
    [props.config.models]
  )
  const suggestions = useQuery({
    queryKey: ['resource-model-names'],
    queryFn: async () =>
      requireServerSuccess(
        (await api.get<{ success: boolean; data: string[] }>('/api/models'))
          .data
      ).data,
    meta: { errorToast: false },
  })
  const options = (Array.isArray(suggestions.data) ? suggestions.data : []).map(
    (value) => ({ value, label: value })
  )
  const table = useReactTable({
    data: names,
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    initialState: { pagination: { pageIndex: 0, pageSize: 20 } },
  })
  const rule = Object.hasOwn(models, selected) ? models[selected] : undefined
  return (
    <fieldset className='min-w-0 space-y-4 rounded-lg border p-4'>
      <legend className='px-1 text-sm font-medium'>
        {t('Model defaults and user overrides')}
      </legend>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Exact request model names before channel mapping. Every user receives their own allowance; total user limits also apply.'
        )}
      </p>
      <div className='flex flex-wrap items-start gap-2'>
        <div className='min-w-48 flex-1'>
          <ComboboxInput
            aria-label={t('Exact model name')}
            placeholder={t('Exact model name')}
            options={options}
            value={name}
            onValueChange={setName}
            allowCustomValue
          />
        </div>
        <Button
          type='button'
          onClick={() => {
            const next = {
              ...props.config,
              models: {
                ...models,
                [name]: { defaults: unlimitedResourceLimits, users: {} },
              },
            }
            if (
              Object.hasOwn(models, name) ||
              !userResourcesConfigSchema.safeParse(next).success
            ) {
              setError('Enter a new valid exact model name.')
              return
            }
            props.onChange(next)
            setSelected(name)
            setName('')
            setError('')
          }}
        >
          {t('Add model rule')}
        </Button>
      </div>
      {name && !options.some((option) => option.value === name) && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'No matching model is currently listed. Check the exact name before saving.'
          )}
        </p>
      )}
      {error && (
        <p role='alert' className='text-destructive text-sm'>
          {t(error)}
        </p>
      )}
      <StaticDataTable
        data={table.getRowModel().rows.map((row) => row.original)}
        getRowKey={(model) => model}
        tableClassName='table-fixed'
        getRowClassName={(model) =>
          model === selected ? 'bg-muted/50' : undefined
        }
        emptyContent={t('No model rules configured.')}
        columns={[
          { id: 'model', header: t('Model'), cell: (model) => model },
          {
            id: 'action',
            className: 'w-24',
            header: t('Actions'),
            cell: (model) => (
              <Button
                type='button'
                variant='outline'
                aria-label={t('Edit model {{model}}', { model })}
                onClick={() => setSelected(model)}
              >
                {t('Edit')}
              </Button>
            ),
          },
        ]}
      />
      <DataTablePagination table={table} compact />
      {rule && (
        <div className='min-w-0 space-y-4 border-t pt-4'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h3 className='text-sm font-medium break-all'>{selected}</h3>
            <Button
              type='button'
              variant='outline'
              onClick={() => setRemoving(true)}
            >
              {t('Remove model rule')}
            </Button>
          </div>
          <ResourceLimitFields
            limits={rule.defaults}
            onChange={(defaults) =>
              props.onChange({
                ...props.config,
                models: {
                  ...models,
                  [selected]: {
                    ...rule,
                    defaults: defaults as UserResourcesConfig['defaults'],
                  },
                },
              })
            }
          />
          <UserResourcesOverridesTable
            config={rule}
            onChange={(next) =>
              props.onChange({
                ...props.config,
                models: {
                  ...models,
                  [selected]: { defaults: next.defaults, users: next.users },
                },
              })
            }
          />
        </div>
      )}
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title={t('Remove model rule')}
        desc={t(
          'New requests stop recording this model. Existing windows and requests expire normally.'
        )}
        confirmText={t('Remove model rule')}
        handleConfirm={() => {
          const next = { ...models }
          delete next[selected]
          props.onChange({ ...props.config, models: next })
          setSelected('')
          setRemoving(false)
        }}
      />
    </fieldset>
  )
}
