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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'

import { getChannels, searchChannels } from '../api'
import { aggregateChannelsByTag, channelsQueryKeys } from '../lib'
import type { SearchChannelsParams } from '../types'

export function useChannelListQuery(params: SearchChannelsParams) {
  const queryClient = useQueryClient()
  const [visible, setVisible] = useState(
    () => document.visibilityState !== 'hidden'
  )
  useEffect(() => {
    const onVisibilityChange = () =>
      setVisible(document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () =>
      document.removeEventListener('visibilitychange', onVisibilityChange)
  }, [])

  const queryKey = channelsQueryKeys.list({ ...params })
  const query = useQuery({
    queryKey,
    queryFn: () => {
      const background = queryClient.getQueryData(queryKey) !== undefined
      const config = {
        skipErrorHandler: background,
        skipBusinessError: background,
      }
      if (params.keyword?.trim() || params.model?.trim()) {
        return searchChannels(params, config)
      }
      return getChannels(params, config)
    },
    meta: { channelListHandlesBackgroundErrors: true },
    retry: false,
    staleTime: 0,
    refetchOnMount: 'always',
    refetchOnWindowFocus: 'always',
    refetchOnReconnect: false,
    refetchInterval: (current) =>
      visible && current.state.fetchStatus === 'idle' ? 60_000 : false,
    refetchIntervalInBackground: false,
    placeholderData: (previousData) => previousData,
  })

  const payload = query.data?.data
  const concurrencyUnavailable = Boolean(
    payload &&
    (query.isError ||
      !payload.in_flight_available ||
      payload.items.some((channel) => channel.in_flight == null))
  )
  const countsUnknown = concurrencyUnavailable || query.isPlaceholderData
  const channels = useMemo(() => {
    const items = payload?.items || []
    const channels = countsUnknown
      ? items.map((channel) => ({ ...channel, in_flight: null }))
      : items
    return params.tag_mode ? aggregateChannelsByTag(channels) : channels
  }, [payload, countsUnknown, params.tag_mode])

  return { ...query, channels, concurrencyUnavailable }
}
