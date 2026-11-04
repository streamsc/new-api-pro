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

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

export function UserAgentCell(props: { value?: string }) {
  const { t } = useTranslation()
  if (!props.value) return <span className='text-muted-foreground'>—</span>

  return (
    <Dialog
      title={t('User Agent')}
      trigger={
        <Button
          variant='ghost'
          className='h-auto max-w-56 min-w-0 justify-start px-0'
          aria-label={t('User Agent')}
        >
          <span className='truncate'>{props.value}</span>
        </Button>
      }
      footer={
        <CopyButton value={props.value} size='sm'>
          {t('Copy')}
        </CopyButton>
      }
    >
      <p className='text-sm break-all whitespace-pre-wrap'>{props.value}</p>
    </Dialog>
  )
}
