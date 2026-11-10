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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { UserResourcesUserPicker } from '@/features/system-settings/request-limits/user-resources-user-picker'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { UserModelResourceTable } from './resource-usage-table'
import { ResourceUsageValues } from './resource-usage-values'
import type { ResourceSnapshot, UserModelResource } from './types'
import { useResourceVisibility } from './use-resource-visibility'

export function AdminResourceOverview() {
  const { t } = useTranslation()
  const [user, setUser] = useState('')
  const [model, setModel] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const visibility = useResourceVisibility()
  const query = useQuery({
    queryKey: ['admin-user-resources', user, model, page, pageSize],
    queryFn: async ({ signal }) =>
      requireServerSuccess(
        (
          await api.get<{
            success: boolean
            data: {
              items: UserModelResource[]
              total: number
              complete: boolean
              mode: string
              user_total?: ResourceSnapshot
            }
          }>(
            `/api/user/resources?user_id=${encodeURIComponent(user)}&model=${encodeURIComponent(model)}&p=${page}&page_size=${pageSize}`,
            { signal }
          )
        ).data
      ).data,
    refetchInterval: visibility.visible ? 5000 : false,
    refetchIntervalInBackground: false,
    meta: { errorToast: false },
  })
  const userTotal = query.data?.user_total
  return (
    <div
      ref={visibility.ref}
      className='min-w-0 space-y-4 rounded-lg border p-4'
    >
      <h2 className='font-medium'>{t('User and model resources')}</h2>
      <div className='flex flex-wrap gap-3'>
        <div className='min-w-48 flex-1'>
          <Label htmlFor='resource-user-filter'>{t('Username')}</Label>
          <UserResourcesUserPicker
            id='resource-user-filter'
            value={user}
            onChange={(value) => {
              setUser(value)
              setPage(1)
            }}
          />
        </div>
        <Input
          className='min-w-48 flex-1'
          aria-label={t('Filter exact model')}
          value={model}
          onChange={(event) => {
            setModel(event.target.value)
            setPage(1)
          }}
          placeholder={t('Filter exact model')}
        />
      </div>
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          title={t('Resource usage temporarily unavailable')}
          onRetry={() => {
            void query.refetch()
          }}
        />
      )}
      {!query.isError && query.data && (
        <>
          <p className='text-muted-foreground text-sm'>
            {query.data.mode === 'redis'
              ? t('Shared Redis counters')
              : t('Current node counters')}
          </p>
          {!query.data.complete && (
            <p role='status' className='text-destructive text-sm'>
              {t(
                'Resource discovery incomplete. Some combinations may be missing.'
              )}
            </p>
          )}
          {userTotal && (
            <div className='space-y-2 rounded border p-3'>
              <h3 className='text-sm font-medium'>{t('User total')}</h3>
              <div className='grid gap-4 sm:grid-cols-3'>
                {(['concurrency', 'rpm', 'input_tpm'] as const).map(
                  (resource) => (
                    <div key={resource} className='text-sm'>
                      <p>
                        {t(
                          {
                            concurrency: 'Concurrency',
                            rpm: 'RPM',
                            input_tpm: 'Estimated input TPM',
                          }[resource]
                        )}
                      </p>
                      <ResourceUsageValues
                        snapshot={userTotal}
                        resource={resource}
                        showSources
                      />
                    </div>
                  )
                )}
              </div>
            </div>
          )}
          <UserModelResourceTable
            rows={query.data.items}
            total={query.data.total}
            page={page}
            pageSize={pageSize}
            onPageSizeChange={setPageSize}
            onPageChange={setPage}
            showUser
            showSources
          />
        </>
      )}
    </div>
  )
}
