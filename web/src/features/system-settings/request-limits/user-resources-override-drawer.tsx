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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
  sideDrawerFormClassName,
  sideDrawerFooterClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
  SheetFooter,
} from '@/components/ui/sheet'

import { ResourceLimitFields } from './user-resources-limit-fields'
import {
  userResourcesConfigSchema,
  type UserResourcesConfig,
} from './user-resources-schema'
import {
  UserResourcesUserPicker,
  UserResourceUserLabel,
} from './user-resources-user-picker'

export function UserResourcesOverrideDrawer(props: {
  config: UserResourcesConfig
  userId: string | null
  onClose: () => void
  onApply: (id: string, limits: UserResourcesConfig['users'][string]) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const [userId, setUserId] = useState(props.userId ?? '')
  const [limits, setLimits] = useState(
    props.config.users[props.userId ?? ''] ?? {}
  )
  const [error, setError] = useState('')
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
    >
      <SheetContent className={sideDrawerContentClassName()}>
        <SheetHeader className={sideDrawerHeaderClassName('shrink-0')}>
          <SheetTitle>
            {props.userId ? t('Edit user override') : t('Add user override')}
          </SheetTitle>
          <SheetDescription>
            {t(
              'Apply changes to the draft, then save the page to publish them.'
            )}
          </SheetDescription>
        </SheetHeader>
        <form
          id={id}
          className={sideDrawerFormClassName()}
          onSubmit={(event) => {
            event.preventDefault()
            event.stopPropagation()
            if (
              !userId ||
              (!props.userId && Object.hasOwn(props.config.users, userId))
            ) {
              setError('Select a user without an existing override.')
              return
            }
            if (
              !userResourcesConfigSchema.safeParse({
                defaults: props.config.defaults,
                users: { [userId]: limits },
              }).success
            ) {
              setError('Invalid resource limits configuration')
              return
            }
            props.onApply(userId, limits)
          }}
        >
          {props.userId ? (
            <div className='text-sm font-medium'>
              <UserResourceUserLabel id={props.userId} />
            </div>
          ) : (
            <div className='space-y-2'>
              <Label htmlFor={`${id}-user`}>{t('Username')}</Label>
              <UserResourcesUserPicker
                id={`${id}-user`}
                value={userId}
                onChange={setUserId}
              />
            </div>
          )}
          <ResourceLimitFields
            stacked
            limits={limits}
            defaults={props.config.defaults}
            onChange={setLimits}
          />
          {error && (
            <p role='alert' className='text-destructive text-sm'>
              {t(error)}
            </p>
          )}
        </form>
        <SheetFooter className={sideDrawerFooterClassName('shrink-0')}>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button type='submit' form={id}>
            {t('Apply')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
