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

import {
  Combobox,
  ComboboxInput,
  ComboboxContent,
  ComboboxList,
  ComboboxItem,
  ComboboxEmpty,
  useComboboxAnchor,
} from '@/components/ui/combobox'
import { getUser, searchUsers } from '@/features/users/api'
import { useDebounce } from '@/hooks/use-debounce'
import { requireServerSuccess } from '@/lib/server-error-message'

type UserOption = { id: number; username: string }

export function UserResourcesUserPicker(props: {
  id: string
  value: string
  onChange: (id: string) => void
}) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<UserOption | null>(null)
  const keyword = useDebounce(search, 250)
  const anchor = useComboboxAnchor()
  const query = useQuery({
    queryKey: ['user-resource-user-search', keyword],
    queryFn: async () =>
      requireServerSuccess(await searchUsers({ keyword, page_size: 20 })).data,
  })
  return (
    <div className='w-full sm:max-w-sm'>
      <Combobox<UserOption>
        items={query.data?.items ?? []}
        filter={null}
        value={props.value ? selected : null}
        itemToStringLabel={(user) => `${user.username} · ID ${user.id}`}
        isItemEqualToValue={(a, b) => a.id === b.id}
        onInputValueChange={(value, details) => {
          if (details.reason === 'input-change') {
            setSearch(value)
            props.onChange('')
          }
        }}
        onValueChange={(user) => {
          setSelected(user)
          props.onChange(user ? String(user.id) : '')
        }}
      >
        <div ref={anchor}>
          <ComboboxInput
            id={props.id}
            placeholder={t('Search by username or ID')}
          />
        </div>
        <ComboboxContent anchor={anchor}>
          <ComboboxEmpty>
            {query.isFetching ? t('Loading...') : t('No results found')}
          </ComboboxEmpty>
          <ComboboxList>
            {(user: UserOption) => (
              <ComboboxItem key={user.id} value={user}>
                {user.username} · ID {user.id}
              </ComboboxItem>
            )}
          </ComboboxList>
        </ComboboxContent>
      </Combobox>
      {query.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Failed to search users')}
        </p>
      )}
    </div>
  )
}

export function UserResourceUserLabel(props: { id: string }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['user-resource-user', props.id],
    queryFn: async () =>
      requireServerSuccess(await getUser(Number(props.id))).data,
    staleTime: 60000,
    retry: false,
  })
  return (
    <>
      {query.data?.username && `${query.data.username} · `}
      {t('User ID')} {props.id}
      {query.isError && (
        <span className='text-muted-foreground ml-2 font-normal'>
          {t('Username unavailable')}
        </span>
      )}
    </>
  )
}
