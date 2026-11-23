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
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { userResourcesConfigSchema } from '../user-resources-schema'
import { UserResourcesSection } from '../user-resources-section'

it('validates and preserves exact model names that match object prototype properties', () => {
  for (const name of ['__proto__', 'constructor', 'toString']) {
    const rule = { defaults: config.defaults, users: {} }
    const parsed = userResourcesConfigSchema.parse({
      ...config,
      models: { [name]: rule },
    })
    expect(Object.hasOwn(parsed.models ?? {}, name)).toBe(true)
    expect(parsed.models?.[name]).toEqual(rule)
    expect(
      userResourcesConfigSchema.safeParse({
        ...config,
        models: {
          [name]: {
            ...rule,
            defaults: { ...config.defaults, rpm: { enabled: true, limit: -1 } },
          },
        },
      }).success
    ).toBe(false)
  }
})

let client: QueryClient
let actions: HTMLDivElement
const config = {
  defaults: {
    concurrency: { enabled: true, limit: 0 },
    input_tpm: { enabled: false, limit: 0 },
    rpm: { enabled: true, limit: 120 },
  },
  users: { '42': { rpm: { enabled: false, limit: 0 } } },
}
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  actions = document.createElement('div')
  document.body.append(actions)
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/user_resources') {
      return { data: { success: true, version: 'initial', data: config } }
    }
    if (url === '/api/models') {
      return {
        data: {
          success: true,
          data: { 1: ['model-A'], 2: ['model-A', 'model-B'], 3: null },
        },
      }
    }
    if (url.startsWith('/api/user/search?')) {
      return {
        data: {
          success: true,
          data: {
            items: [
              { id: 42, username: 'alice' },
              { id: 43, username: 'bob' },
            ],
            total: 2,
          },
        },
      }
    }
    const id = Number(url.split('/').at(-1))
    return {
      data: {
        success: true,
        data: { id, username: id === 42 ? 'alice' : 'bob' },
      },
    }
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  actions.remove()
})
function mount() {
  render(
    <QueryClientProvider client={client}>
      <SettingsPageProvider actionsContainer={actions}>
        <UserResourcesSection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

async function selectScheduleModel(
  user: ReturnType<typeof userEvent.setup>,
  name: string
) {
  await user.click(
    await screen.findByRole('tab', { name: 'Model rate limit schedules' })
  )
  const model = screen.getByRole('combobox', { name: 'Model' })
  await user.type(model, name)
  await screen.findByRole('option', { name })
  await user.keyboard('{ArrowDown}{Enter}')
  expect(model).toHaveValue(name)
}

it('keeps a model schedule through JSON edits and a failed save, and can remove the schedule', async () => {
  const put = vi.spyOn(api, 'put').mockRejectedValue(new Error('Save failed'))
  const user = userEvent.setup()
  mount()
  await selectScheduleModel(user, 'model-A')
  await user.click(screen.getByRole('button', { name: 'Add time period' }))
  const timezone = screen.getByRole('combobox', { name: 'Timezone' })
  await user.clear(timezone)
  await user.type(timezone, 'Asia/Shanghai')
  await screen.findByRole('option', { name: 'Asia/Shanghai' })
  await user.keyboard('{ArrowDown}{Enter}')
  fireEvent.change(screen.getByLabelText('Start time'), {
    target: { value: '08:00' },
  })
  fireEvent.change(screen.getByRole('spinbutton', { name: 'Multiplier' }), {
    target: { value: '0.1' },
  })

  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  const editor = screen.getByRole<HTMLTextAreaElement>('textbox', {
    name: 'Defaults and user overrides',
  })
  const draft = JSON.parse(editor.value)
  expect(draft.models['model-A'].schedule).toEqual({
    timezone: 'Asia/Shanghai',
    periods: [{ start: '08:00', end: '21:00', multiplier: 0.1 }],
  })
  draft.models['model-A'].schedule.periods[0].start = '07:00'
  fireEvent.input(editor, { target: { value: JSON.stringify(draft) } })
  await user.click(
    screen.getByRole('tab', { name: 'Model rate limit schedules' })
  )
  expect(screen.getByLabelText('Start time')).toHaveValue('07:00')
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Peak hours'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledExactlyOnceWith('/api/option/user_resources', {
      expected_version: 'initial',
      config: draft,
      reason: 'Peak hours',
    })
  )
  expect(screen.getByRole('spinbutton', { name: 'Multiplier' })).toHaveValue(
    0.1
  )
  expect(
    screen.getByRole('textbox', { name: 'Reason for change' })
  ).toHaveValue('Peak hours')

  await user.click(screen.getByRole('button', { name: 'Remove time period 1' }))
  expect(
    screen.queryByRole('group', { name: 'Time period 1' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  expect(
    JSON.parse(
      screen.getByRole<HTMLTextAreaElement>('textbox', {
        name: 'Defaults and user overrides',
      }).value
    ).models['model-A']
  ).not.toHaveProperty('schedule')
})

it('associates invalid times and multipliers with fields and rejects overlapping overnight periods', async () => {
  const put = vi.spyOn(api, 'put')
  const user = userEvent.setup()
  mount()
  await selectScheduleModel(user, 'model-A')
  const addPeriod = screen.getByRole('button', { name: 'Add time period' })
  await user.click(addPeriod)
  const first = within(screen.getByRole('group', { name: 'Time period 1' }))
  const start = first.getByLabelText('Start time')
  const end = first.getByLabelText('End time')
  const multiplier = first.getByRole('spinbutton', { name: 'Multiplier' })
  fireEvent.change(start, { target: { value: '' } })
  expect(start).toHaveAccessibleDescription('Enter a valid time.')
  fireEvent.change(start, { target: { value: '21:00' } })
  expect(end).toHaveAccessibleDescription('Start and end times must differ.')
  fireEvent.change(multiplier, { target: { value: '0' } })
  expect(multiplier).toHaveAccessibleDescription('Enter a positive multiplier.')
  fireEvent.change(multiplier, { target: { value: '0.1' } })
  fireEvent.change(start, { target: { value: '22:00' } })
  fireEvent.change(end, { target: { value: '02:00' } })
  expect(first.getByText('Crosses midnight')).toBeVisible()

  await user.click(addPeriod)
  const second = within(screen.getByRole('group', { name: 'Time period 2' }))
  fireEvent.change(second.getByLabelText('Start time'), {
    target: { value: '01:00' },
  })
  fireEvent.change(second.getByLabelText('End time'), {
    target: { value: '03:00' },
  })
  expect(first.getByRole('alert')).toHaveTextContent(
    'This time period overlaps another period.'
  )
  expect(second.getByRole('alert')).toHaveTextContent(
    'This time period overlaps another period.'
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Overnight'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText('Invalid resource limits configuration')
  ).toBeVisible()
  expect(put).not.toHaveBeenCalled()
})
it('previews scaled model defaults and shows both ends of an overnight period', async () => {
  const scheduled = {
    timezone: 'Asia/Shanghai',
    periods: [{ start: '07:00', end: '21:00', multiplier: 0.1 }],
  }
  vi.mocked(api.get).mockResolvedValueOnce({
    data: {
      success: true,
      version: 'initial',
      data: {
        ...config,
        models: {
          'model-A': {
            defaults: {
              concurrency: { enabled: true, limit: 20 },
              input_tpm: { enabled: true, limit: 100000 },
              rpm: { enabled: true, limit: 120 },
            },
            users: { '42': { rpm: { enabled: true, limit: 57 } } },
            schedule: scheduled,
          },
          'model-B': {
            defaults: {
              concurrency: { enabled: true, limit: 3 },
              input_tpm: { enabled: true, limit: 0 },
              rpm: { enabled: false, limit: 0 },
            },
            users: {},
            schedule: scheduled,
          },
        },
      },
    },
  })
  const user = userEvent.setup()
  mount()
  await selectScheduleModel(user, 'model-A')
  const timeline = screen.getByRole('figure', { name: /24-hour timeline/ })
  expect(
    within(timeline).getByRole('img', { name: 'Base limits 00:00–07:00' })
  ).toBeVisible()
  expect(
    within(timeline).getByRole('img', { name: 'Time period 1 07:00–21:00' })
  ).toBeVisible()
  expect(
    within(timeline).getByRole('img', { name: 'Base limits 21:00–24:00' })
  ).toBeVisible()
  expect(
    within(screen.getByRole('group', { name: 'Model default limits preview' }))
      .getAllByRole('definition')
      .map((element) => element.textContent)
  ).toEqual(['2', '10,000', '12'])

  fireEvent.change(screen.getByLabelText('Start time'), {
    target: { value: '22:00' },
  })
  fireEvent.change(screen.getByLabelText('End time'), {
    target: { value: '02:00' },
  })
  expect(
    within(timeline).getByRole('img', { name: 'Time period 1 00:00–02:00' })
  ).toBeVisible()
  expect(
    within(timeline).getByRole('img', { name: 'Base limits 02:00–22:00' })
  ).toBeVisible()
  expect(
    within(timeline).getByRole('img', { name: 'Time period 1 22:00–24:00' })
  ).toBeVisible()
  await selectScheduleModel(user, 'model-B')
  expect(
    within(screen.getByRole('group', { name: 'Model default limits preview' }))
      .getAllByRole('definition')
      .map((element) => element.textContent)
  ).toEqual(['1', '0', 'Unlimited'])
})

it('requires a reason and saves explicit zero and unlimited overrides', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('tab', { name: 'JSON' }))
  await screen.findByRole('textbox', { name: 'Defaults and user overrides' })
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(put).not.toHaveBeenCalled()
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Capacity review'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/option/user_resources', {
      config,
      expected_version: 'initial',
      reason: 'Capacity review',
    })
  )
})
it('rejects negative resource limits without sending a write', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('tab', { name: 'JSON' }))
  const editor = await screen.findByRole('textbox', {
    name: 'Defaults and user overrides',
  })
  fireEvent.input(editor, {
    target: {
      value: JSON.stringify({
        ...config,
        defaults: { ...config.defaults, rpm: { enabled: true, limit: -1 } },
      }),
    },
  })
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Review'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText('Invalid resource limits configuration')
  ).toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
})
it('retains the change reason and edited config after a failed save', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Store unavailable' },
  })
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('tab', { name: 'JSON' }))
  const editor = await screen.findByRole('textbox', {
    name: 'Defaults and user overrides',
  })
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Keep this reason'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  expect(
    screen.getByRole('textbox', { name: 'Reason for change' })
  ).toHaveValue('Keep this reason')
  expect(editor).toHaveValue(JSON.stringify(config, null, 2))
})

it('edits in a drawer, cancels without changing the draft and applies without publishing', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('button', { name: 'Edit 42' }))
  let drawer = screen.getByRole('dialog')
  await user.selectOptions(
    within(drawer).getByRole('combobox', { name: 'Concurrency' }),
    'limited'
  )
  fireEvent.change(
    within(drawer).getByRole('spinbutton', { name: 'Concurrency Limit' }),
    { target: { value: '7' } }
  )
  await user.click(within(drawer).getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Edit 42' }))
  drawer = screen.getByRole('dialog')
  expect(
    within(drawer).getByRole('combobox', { name: 'Concurrency' })
  ).toHaveValue('inherit')
  await user.selectOptions(
    within(drawer).getByRole('combobox', { name: 'Concurrency' }),
    'limited'
  )
  await user.click(within(drawer).getByRole('button', { name: 'Apply' }))
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  expect(
    JSON.parse(
      (
        screen.getByRole('textbox', {
          name: 'Defaults and user overrides',
        }) as HTMLTextAreaElement
      ).value
    ).users['42']
  ).toEqual({
    concurrency: { enabled: true, limit: 0 },
    rpm: { enabled: false, limit: 0 },
  })
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Review limits'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
})

it('adds a selected username using its ID and rejects duplicate overrides', async () => {
  const user = userEvent.setup()
  mount()
  await user.click(
    await screen.findByRole('button', { name: 'Add user override' })
  )
  await user.click(
    within(screen.getByRole('dialog')).getByRole('combobox', {
      name: 'Username',
    })
  )
  await user.click(await screen.findByRole('option', { name: 'alice · ID 42' }))
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Select a user without an existing override.'
  )
  await user.click(
    within(screen.getByRole('dialog')).getByRole('combobox', {
      name: 'Username',
    })
  )
  await user.click(await screen.findByRole('option', { name: 'bob · ID 43' }))
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(await screen.findByRole('button', { name: 'Edit 43' })).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  expect(
    JSON.parse(
      (
        screen.getByRole('textbox', {
          name: 'Defaults and user overrides',
        }) as HTMLTextAreaElement
      ).value
    ).users
  ).toEqual({ ...config.users, '43': {} })
})

it('paginates 1001 overrides and only fetches usernames for visible rows', async () => {
  const users = Object.fromEntries(
    Array.from({ length: 1001 }, (_, i) => [String(i + 1), {}])
  )
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url === '/api/option/user_resources') {
      return { data: { success: true, data: { ...config, users } } }
    }
    if (url.startsWith('/api/user/search?')) {
      return {
        data: {
          success: true,
          data: { items: [{ id: 1001, username: 'last-user' }], total: 1 },
        },
      }
    }
    const id = Number(url.split('/').at(-1))
    return { data: { success: true, data: { id, username: `user-${id}` } } }
  })
  const user = userEvent.setup()
  mount()
  await screen.findByRole('button', { name: 'Edit 20' })
  expect(
    within(
      screen.getByRole('region', { name: 'Defaults and user overrides' })
    ).getAllByRole('row')
  ).toHaveLength(21)
  expect(
    screen.queryByRole('button', { name: 'Edit 21' })
  ).not.toBeInTheDocument()
  const userCalls = () =>
    vi
      .mocked(api.get)
      .mock.calls.filter(([url]) => /^\/api\/user\/\d+$/.test(url))
  await waitFor(() => expect(userCalls()).toHaveLength(20))
  const overrideSection = screen.getByRole('region', {
    name: 'Defaults and user overrides',
  }).parentElement
  if (!overrideSection) throw new Error('Override section is missing')
  await user.click(
    within(overrideSection).getByRole('button', { name: 'Go to next page' })
  )
  await screen.findByRole('button', { name: 'Edit 40' })
  await waitFor(() => expect(userCalls()).toHaveLength(40))
  await user.type(
    screen.getByRole('combobox', { name: 'Find user override' }),
    'last-user'
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/user/search?keyword=last-user&group=&p=1&page_size=20'
    )
  )
  await user.click(
    await screen.findByRole('option', { name: 'last-user · ID 1001' })
  )
  expect(await screen.findByRole('button', { name: 'Edit 1001' })).toBeVisible()
  expect(
    within(
      screen.getByRole('region', { name: 'Defaults and user overrides' })
    ).getAllByRole('row')
  ).toHaveLength(2)
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  expect(await screen.findByRole('button', { name: 'Edit 1' })).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  expect(
    Object.keys(
      JSON.parse(
        (
          screen.getByRole('textbox', {
            name: 'Defaults and user overrides',
          }) as HTMLTextAreaElement
        ).value
      ).users
    )
  ).toHaveLength(1001)
})

it('confirms removal and preserves invalid JSON when switching modes', async () => {
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('button', { name: 'Actions 42' }))
  await user.click(screen.getByRole('menuitem', { name: 'Remove override' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Remove override',
    })
  )
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  const editor = screen.getByRole('textbox', {
    name: 'Defaults and user overrides',
  })
  expect(JSON.parse((editor as HTMLTextAreaElement).value).users).toEqual({})
  fireEvent.input(editor, { target: { value: '{invalid' } })
  await user.click(screen.getByRole('tab', { name: 'Visual' }))
  expect(editor).toHaveValue('{invalid')
  expect(screen.getByRole('tab', { name: 'JSON' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
})

it('adds an exact model with personal defaults and saves user overrides in the same audited draft', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, version: 'saved' } })
  const user = userEvent.setup()
  mount()
  await user.type(
    await screen.findByRole('combobox', { name: 'Exact model name' }),
    'model-A'
  )
  await user.click(screen.getByRole('button', { name: 'Add model rule' }))
  const modelSection = screen.getByRole('group', {
    name: 'Model defaults and user overrides',
  })
  await user.selectOptions(
    within(modelSection).getByRole('combobox', { name: 'Concurrency' }),
    'limited'
  )
  fireEvent.change(
    within(modelSection).getByRole('spinbutton', { name: 'Concurrency Limit' }),
    { target: { value: '2' } }
  )
  await selectScheduleModel(user, 'model-A')
  await user.click(screen.getByRole('button', { name: 'Add time period' }))
  await user.click(screen.getByRole('tab', { name: 'Visual' }))
  await user.click(screen.getByRole('button', { name: 'Edit model model-A' }))
  const updatedModelSection = screen.getByRole('group', {
    name: 'Model defaults and user overrides',
  })
  await user.click(
    within(updatedModelSection).getByRole('button', {
      name: 'Add user override',
    })
  )
  const drawer = screen.getByRole('dialog')
  await user.click(within(drawer).getByRole('combobox', { name: 'Username' }))
  await user.click(await screen.findByRole('option', { name: 'bob · ID 43' }))
  await user.selectOptions(
    within(drawer).getByRole('combobox', { name: 'RPM' }),
    'limited'
  )
  fireEvent.change(
    within(drawer).getByRole('spinbutton', { name: 'RPM Limit' }),
    { target: { value: '0' } }
  )
  await user.click(within(drawer).getByRole('button', { name: 'Apply' }))
  expect(put).not.toHaveBeenCalled()
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Low capacity'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  expect(put.mock.calls[0][1]).toMatchObject({
    expected_version: 'initial',
    reason: 'Low capacity',
    config: {
      models: {
        'model-A': {
          defaults: { concurrency: { enabled: true, limit: 2 } },
          users: { '43': { rpm: { enabled: true, limit: 0 } } },
          schedule: {
            timezone: 'UTC',
            periods: [{ start: '07:00', end: '21:00', multiplier: 1 }],
          },
        },
      },
    },
  })
})

it('retains model rules and the original version after a conflicting save', async () => {
  const put = vi.spyOn(api, 'put').mockRejectedValue({
    response: {
      status: 409,
      data: {
        success: false,
        message: 'Resource limits changed. Reload before saving.',
      },
    },
  })
  const user = userEvent.setup()
  mount()
  await user.type(
    await screen.findByRole('combobox', { name: 'Exact model name' }),
    'model-only'
  )
  await user.click(screen.getByRole('button', { name: 'Add model rule' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Reason for change' }),
    'Keep draft'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  expect(
    screen.getByRole('textbox', { name: 'Reason for change' })
  ).toHaveValue('Keep draft')
  expect(
    screen.getByRole('button', { name: 'Edit model model-only' })
  ).toBeVisible()
  expect(put.mock.calls[0][1]).toMatchObject({ expected_version: 'initial' })
})

it('requires confirmation to remove a model and keeps removal in the unsaved draft', async () => {
  const put = vi.spyOn(api, 'put')
  const user = userEvent.setup()
  mount()
  await user.type(
    await screen.findByRole('combobox', { name: 'Exact model name' }),
    'temporary-model'
  )
  await user.click(screen.getByRole('button', { name: 'Add model rule' }))
  const section = screen.getByRole('group', {
    name: 'Model defaults and user overrides',
  })
  expect(
    within(section).getByRole('row', { name: 'temporary-model Edit' })
  ).toHaveClass('bg-muted/50')
  await user.click(
    within(section).getByRole('button', { name: 'Remove model rule' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  expect(
    screen.getByRole('button', { name: 'Edit model temporary-model' })
  ).toBeVisible()
  await user.click(
    within(section).getByRole('button', { name: 'Remove model rule' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Remove model rule',
    })
  )
  expect(
    screen.queryByRole('button', { name: 'Edit model temporary-model' })
  ).not.toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('tab', { name: 'JSON' }))
  const editor = screen.getByRole('textbox', {
    name: 'Defaults and user overrides',
  }) as HTMLTextAreaElement
  expect(JSON.parse(editor.value).models).toEqual({})
})
