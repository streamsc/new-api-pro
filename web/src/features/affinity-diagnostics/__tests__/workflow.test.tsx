import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { AffinityDiagnostics } from '../index'
import { diagnosticSchema } from '../lib/schema'

const task = {
  id: 1,
  user_id: 17,
  status: 'running',
  reason: '',
  started_at: Date.now(),
  deadline: Date.now() + 600000,
  expires_at: Date.now() + 3600000,
  max_count: 100,
  max_bytes: 20971520,
  requested_count: 2,
  requested_bytes: 1000,
  saved_count: 1,
  saved_bytes: 500,
  skipped_body: 0,
  dropped_count: 1,
  queue_dropped: 1,
  write_dropped: 0,
  admission_dropped: 0,
  stats_incomplete: false,
  cleanup_error: false,
}
const clients: QueryClient[] = []
function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <AffinityDiagnostics userId={17} />
    </QueryClientProvider>
  )
  return client
}
afterEach(() => {
  cleanup()
  for (const client of clients.splice(0)) client.clear()
  vi.restoreAllMocks()
})

it('requires an explicit confirmation and keeps full-body capture opt-in', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 0 } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: task } })
  renderPage()
  expect(
    screen.getByRole('checkbox', {
      name: 'Capture complete JSON bodies without redaction',
    })
  ).not.toBeChecked()
  await userEvent.click(
    screen.getByRole('checkbox', {
      name: 'Capture complete JSON bodies without redaction',
    })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Start diagnostic task' })
  )
  const confirmation = await screen.findByRole('alertdialog')
  expect(post).not.toHaveBeenCalled()
  expect(within(confirmation).getByText(/stored unchanged/)).toBeVisible()
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Continue' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/affinity-diagnostics',
      expect.objectContaining({
        user_id: 17,
        capture_body: true,
        max_count: 100,
        duration_seconds: 600,
      })
    )
  )
})

it('paginates tasks on the server and disables stop for a stopped task', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { items: [{ ...task, status: 'stopped' }], total: 21 },
    },
  })
  renderPage()
  expect(await screen.findByRole('button', { name: 'Stop' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      '/api/affinity-diagnostics',
      expect.objectContaining({ params: { page: 2, page_size: 20 } })
    )
  )
})

it('fetches sample details only on demand and reports a missing body without offering a download', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/affinity-diagnostics') {
      return { data: { success: true, data: { items: [task], total: 1 } } }
    }
    if (url === '/api/affinity-diagnostics/1/samples') {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                id: 8,
                request_id: 'req-8',
                model: 'test',
                path: '/v1/responses',
                body_status: 'over_limit',
                body_kind: 'http_json',
                size_bytes: 500,
              },
            ],
            total: 1,
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          sample: { id: 8, body_status: 'over_limit' },
          metadata: { body_status: 'over_limit' },
          body: '',
        },
      },
    }
  })
  const client = renderPage()
  await userEvent.click(await screen.findByRole('button', { name: 'Samples' }))
  const view = await screen.findByRole('button', { name: 'View sample' })
  expect(get).not.toHaveBeenCalledWith('/api/affinity-diagnostics/1/samples/8')
  await userEvent.click(view)
  const dialog = await screen.findByRole('dialog', {
    name: 'Diagnostic sample',
  })
  expect(
    await within(dialog).findByText(/Diagnostic body was not collected/)
  ).toBeVisible()
  expect(
    within(dialog).queryByRole('button', { name: 'Download original body' })
  ).not.toBeInTheDocument()
  get.mockRejectedValueOnce(
    new Error('Diagnostic task is unavailable or expired')
  )
  await client.invalidateQueries({ queryKey: ['diagnostic-samples'] })
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
})

it('keeps a failed deletion reviewable instead of reporting success', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [task], total: 1 } },
  })
  const remove = vi.spyOn(api, 'delete').mockResolvedValue({
    data: { success: false, message: 'Database unavailable' },
  })
  renderPage()
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Continue',
    })
  )
  await waitFor(() => expect(remove).toHaveBeenCalled())
  expect(screen.getByRole('alertdialog')).toBeVisible()
})

it('rejects limits above the approved first-release ceilings', () => {
  const values = {
    user_id: 17,
    duration_seconds: 600,
    max_count: 100,
    max_body_bytes: 1048576,
    max_bytes: 20971520,
    capture_body: true,
    excluded_headers: '',
  }
  expect(diagnosticSchema.safeParse(values).success).toBe(true)
  for (const changes of [
    { duration_seconds: 601 },
    { max_count: 101 },
    { max_body_bytes: 1048577 },
    { max_bytes: 20971521 },
    { user_id: 0 },
  ]) {
    expect(diagnosticSchema.safeParse({ ...values, ...changes }).success).toBe(
      false
    )
  }
})
