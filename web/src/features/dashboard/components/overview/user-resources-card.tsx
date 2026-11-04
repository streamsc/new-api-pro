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
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Progress } from '@/components/ui/progress'
import { api } from '@/lib/api'
import { formatNumber, formatTimestamp } from '@/lib/format'
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
  const userId = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    queryKey: ['self-resources', userId],
    queryFn: async ({ signal }) =>
      requireServerSuccess(
        (
          await api.get<{ success: boolean; data: UserResources }>(
            '/api/user/self/resources',
            { signal }
          )
        ).data
      ).data,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
    meta: { errorToast: false },
  })
  const data = query.data
  const usages = data?.usage
  return (
    <PanelWrapper
      title={t('My API resources')}
      loading={query.isPending}
      height='h-40'
      className='max-w-2xl'
      description={data ? `${data.username} · ID ${data.user_id}` : undefined}
    >
      {query.isError || !data?.available || !usages ? (
        <ErrorState
          className='min-h-24'
          title={t('Resource usage temporarily unavailable')}
          onRetry={() => {
            void query.refetch()
          }}
        />
      ) : (
        <div className='space-y-3'>
          {resources.map(([key, label]) => {
            const limit = data.limits[key]
            const usage = usages[key]
            let percent: number | null = null
            let status = 'Unlimited'
            let tone = 'text-muted-foreground'
            if (limit.enabled) {
              percent = limit.limit === 0 ? 100 : (usage / limit.limit) * 100
              status = 'Normal'
              tone =
                'text-success [&_[data-slot=progress-indicator]]:bg-success'
              if (percent >= 80) {
                status = 'Near or at limit'
                tone =
                  'text-destructive [&_[data-slot=progress-indicator]]:bg-destructive'
              } else if (percent >= 40) {
                status = 'Elevated usage'
                tone =
                  'text-[color-mix(in_oklch,var(--warning)_65%,var(--foreground))] dark:text-warning [&_[data-slot=progress-indicator]]:bg-warning'
              }
            }
            return (
              <div key={key} className={tone}>
                <div className='flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs'>
                  <span className='text-foreground font-medium'>
                    {t(label)}
                  </span>
                  <span>
                    {formatNumber(usage)} /{' '}
                    {limit.enabled ? formatNumber(limit.limit) : t('Unlimited')}
                    {percent !== null &&
                      ` · ${t(status)} · ${formatNumber(percent)}%`}
                  </span>
                </div>
                {percent !== null && (
                  <Progress
                    className='mt-1'
                    value={Math.min(percent, 100)}
                    aria-label={t(label)}
                  />
                )}
              </div>
            )
          })}
        </div>
      )}
      <p className='text-muted-foreground mt-3 text-xs'>
        {t(
          'Shared across all API keys and supported models. Async tasks and Realtime are excluded. Contact your administrator to adjust limits.'
        )}
      </p>
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
  )
}
