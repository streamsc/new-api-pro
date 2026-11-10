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
import { Input } from '@/components/ui/input'
import { UserModelResourceTable } from '@/features/user-resources/resource-usage-table'
import { ResourceUsageMeter } from '@/features/user-resources/resource-usage-values'
import type { UserModelResource } from '@/features/user-resources/types'
import { useResourceVisibility } from '@/features/user-resources/use-resource-visibility'
import { api } from '@/lib/api'
import { formatTimestamp } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { PanelWrapper } from '../ui/panel-wrapper'

export type ResourceLimit = { enabled: boolean; limit: number }
export type ResourceLimits = Record<
  'concurrency' | 'input_tpm' | 'rpm',
  ResourceLimit
>
export type UserResources = {
  user_id: number
  username: string
  sources?: Record<keyof ResourceLimits, string>
  models?: UserModelResource[]
  models_total?: number
  models_complete?: boolean
  limits: ResourceLimits
  usage?: Record<keyof ResourceLimits, number>
  available: boolean
  updated_at: number
  mode: 'memory' | 'redis'
}
const resources = [
  ['concurrency', 'Concurrency'],
  ['input_tpm', 'Input TPM (estimated, last 60 seconds)'],
  ['rpm', 'RPM (last 60 seconds)'],
] as const
export function UserResourcesCard() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [model, setModel] = useState('')
  const visibility = useResourceVisibility()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    queryKey: ['self-resources', userId, model, page, pageSize],
    queryFn: async ({ signal }) =>
      requireServerSuccess(
        (
          await api.get<{ success: boolean; data: UserResources }>(
            `/api/user/self/resources?p=${page}&page_size=${pageSize}&model=${encodeURIComponent(model)}`,
            { signal }
          )
        ).data
      ).data,
    refetchInterval: visibility.visible ? 5000 : false,
    refetchIntervalInBackground: false,
    meta: { errorToast: false },
  })
  const data = query.data
  const usages = data?.usage
  return (
    <div ref={visibility.ref}>
      <PanelWrapper
        title={t('My API resources')}
        loading={query.isPending}
        height='h-auto'
        className='min-w-0'
        description={data ? `${data.username} · ID ${data.user_id}` : undefined}
      >
        {query.isError || !data ? (
          <ErrorState
            className='min-h-24'
            title={t('Resource usage temporarily unavailable')}
            onRetry={() => {
              void query.refetch()
            }}
          />
        ) : (
          <div className='space-y-3'>
            {(!data.available || !usages) && (
              <p role='status' className='text-destructive text-sm'>
                {t('Resource usage temporarily unavailable')}
              </p>
            )}
            {resources.map(([key, label]) => (
              <ResourceUsageMeter
                key={key}
                snapshot={data}
                resource={key}
                label={t(label)}
                showLabel
              />
            ))}
          </div>
        )}
        <div className='mt-5 space-y-3 border-t pt-4'>
          <h3 className='text-sm font-medium'>{t('My model resources')}</h3>
          <Input
            aria-label={t('Filter exact model')}
            placeholder={t('Filter exact model')}
            value={model}
            onChange={(event) => {
              setModel(event.target.value)
              setPage(1)
            }}
          />
          {data?.models_complete === false && (
            <p role='status' className='text-destructive text-sm'>
              {t(
                'Resource discovery incomplete. Some combinations may be missing.'
              )}
            </p>
          )}
          {!query.isError && (
            <UserModelResourceTable
              rows={data?.models ?? []}
              total={data?.models_total ?? 0}
              page={page}
              pageSize={pageSize}
              onPageSizeChange={setPageSize}
              onPageChange={setPage}
              showUtilization
            />
          )}
        </div>
        <p className='text-muted-foreground mt-3 text-xs'>
          {t(
            'Shared across all API keys and supported models. Async tasks and Realtime are excluded. Contact your administrator to adjust limits.'
          )}
        </p>
        {data?.mode === 'redis' && (
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Shared Redis counters')}
          </p>
        )}
        {data?.mode === 'memory' && (
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Single process counters reset on restart.')}
          </p>
        )}
        {data && (
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Last updated')}: {formatTimestamp(data.updated_at)}
          </p>
        )}
      </PanelWrapper>
    </div>
  )
}
