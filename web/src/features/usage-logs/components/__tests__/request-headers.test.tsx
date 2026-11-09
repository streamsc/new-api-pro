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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const clients: QueryClient[] = []
const snapshot: NonNullable<
  NonNullable<LogOtherData['admin_info']>['request_headers']
> = {
  source: 'http',
  headers: {
    host: ['gateway.example'],
    'x-trace': ['first', `<script>alert(1)</script>${'客户端'.repeat(300)}`],
  },
  incomplete: true,
  skipped: { field_bytes: 1 },
}

function renderDetails(isAdmin: boolean, requestHeaders = snapshot) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  clients.push(client)
  const log: UsageLog = {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: '',
    model_name: 'test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 0,
    channel_name: '',
    token_id: 0,
    group: '',
    ip: '',
    request_id: '',
    upstream_request_id: '',
    other: JSON.stringify({ admin_info: { request_headers: requestHeaders } }),
  }
  return render(
    <QueryClientProvider client={client}>
      <DetailsDialog
        log={log}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
})

test('administrators can read and copy complete multi-value headers as escaped plain text', async () => {
  const user = userEvent.setup()
  const view = renderDetails(true)
  const content = screen.getByLabelText('Inbound request headers')
  expect(content.textContent).toBe(JSON.stringify(snapshot, null, 2))
  expect(content).toHaveClass(
    'max-h-64',
    'overflow-auto',
    'whitespace-pre-wrap',
    'break-all'
  )
  expect(view.container.querySelector('script')).toBeNull()
  expect(screen.getByRole('status')).toHaveTextContent(
    'Some headers were skipped'
  )
  const copy = screen.getByRole('button', {
    name: 'Copy inbound request headers',
  })
  copy.focus()
  await user.keyboard('{Enter}')
  expect(await navigator.clipboard.readText()).toBe(
    JSON.stringify(snapshot, null, 2)
  )
})

test('ordinary users cannot see or copy administrator headers even if supplied in the response', () => {
  renderDetails(false)
  expect(
    screen.queryByLabelText('Inbound request headers')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Copy inbound request headers' })
  ).not.toBeInTheDocument()
})

test('WebSocket snapshots identify handshake reuse and complete empty snapshots have no warning', () => {
  renderDetails(true, {
    source: 'websocket_handshake',
    headers: {},
    incomplete: false,
    skipped: {},
  })
  expect(
    screen.getByText(
      'Headers from the WebSocket handshake; reused for each request on this connection.'
    )
  ).toBeVisible()
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(
    screen.getByLabelText('Inbound request headers').textContent
  ).toContain('"headers": {}')
})
