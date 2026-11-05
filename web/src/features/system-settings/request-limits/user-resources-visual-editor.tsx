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
import { useTranslation } from 'react-i18next'

import { ResourceLimitFields } from './user-resources-limit-fields'
import { UserResourcesOverridesTable } from './user-resources-overrides-table'
import type { UserResourcesConfig } from './user-resources-schema'

export function UserResourcesVisualEditor(props: {
  config: UserResourcesConfig
  onChange: (config: UserResourcesConfig) => void
}) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0 space-y-6'>
      <fieldset className='space-y-4 rounded-lg border p-4'>
        <legend className='px-1 text-sm font-medium'>
          {t('Default limits')}
        </legend>
        <ResourceLimitFields
          limits={props.config.defaults}
          onChange={(defaults) =>
            props.onChange({
              ...props.config,
              defaults: defaults as UserResourcesConfig['defaults'],
            })
          }
        />
        <p className='text-muted-foreground text-sm'>
          {t(
            'A limit of 0 blocks new requests. RPM and input TPM use a rolling 60-second window.'
          )}
        </p>
      </fieldset>
      <UserResourcesOverridesTable
        config={props.config}
        onChange={props.onChange}
      />
    </div>
  )
}
