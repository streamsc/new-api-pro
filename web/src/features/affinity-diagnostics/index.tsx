import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getCoreRowModel,
  useReactTable,
  type PaginationState,
} from '@tanstack/react-table'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { CodeBlock } from '@/components/ai-elements/code-block'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { DataTablePagination } from '@/components/data-table/core/pagination'
import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { formatNumber } from '@/lib/format'

import {
  changeTask,
  createTask,
  downloadSample,
  extractSample,
  getSample,
  listSamples,
  listTasks,
} from './api'
import {
  diagnosticSchema,
  extractionSchema,
  type DiagnosticFormValues,
  type ExtractionFormValues,
} from './lib/schema'
import type { DiagnosticTask } from './types'

const diagnosticLabels: Record<string, string> = {
  starting: 'Starting',
  running: 'Running',
  stopped: 'Stopped',
  deleting: 'Deleting',
  manual_stop: 'Stopped by administrator',
  manual_delete: 'Deleted by administrator',
  deadline: 'Time limit reached',
  count_limit: 'Sample limit reached',
  byte_limit: 'Sample storage limit reached',
  redis_state_lost: 'Redis state lost',
  redis_unavailable: 'Redis unavailable',
  process_restart: 'Process restarted',
  initialization_failed: 'Initialization failed',
  expired: 'Expired',
  captured: 'Captured',
  not_collected: 'Not collected',
  over_limit: 'Over limit',
  unsupported: 'Unsupported',
  read_failed: 'Read failed',
  missing: 'Missing',
  empty: 'Empty',
  type_mismatch: 'Type mismatch',
  extracted: 'Extracted',
  http_json: 'HTTP JSON',
  responses_ws_normalized: 'Responses WebSocket normalized JSON',
}

export function AffinityDiagnostics(props: { userId?: number }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 20,
  })
  const [selected, setSelected] = useState<DiagnosticTask | null>(null)
  const [action, setAction] = useState<{
    task: DiagnosticTask
    remove: boolean
  } | null>(null)
  const tasks = useQuery({
    queryKey: ['affinity-diagnostics', pagination],
    queryFn: () => listTasks(pagination.pageIndex + 1, pagination.pageSize),
    refetchInterval: 2000,
  })
  const mutation = useMutation({
    mutationFn: (value: { task: DiagnosticTask; remove: boolean }) =>
      changeTask(value.task.id, value.remove),
    onSuccess: () => {
      setAction(null)
      setSelected(null)
      void client.invalidateQueries({ queryKey: ['affinity-diagnostics'] })
    },
  })
  const table = useReactTable({
    data: tasks.data?.items ?? [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    rowCount: tasks.data?.total ?? 0,
    state: { pagination },
    onPaginationChange: setPagination,
  })
  const current =
    tasks.data?.items?.find((item) => item.id === selected?.id) ?? selected
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Affinity diagnostics')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-6'>
          <Alert>
            <AlertDescription>
              {t(
                'Diagnostic samples expire one hour after task creation. Start and stop propagation takes up to two seconds. Redis state loss ends the task; some failure statistics may be unavailable.'
              )}
            </AlertDescription>
          </Alert>
          {tasks.data?.redis_paused ? (
            <Alert>
              <AlertDescription>
                {t(
                  'This node has paused diagnostic capture because Redis is unavailable.'
                )}
                : {tasks.data.node_name}
              </AlertDescription>
            </Alert>
          ) : null}
          <CreateDiagnostic
            userId={props.userId}
            onCreated={(task) => {
              setSelected(task)
              void client.invalidateQueries({
                queryKey: ['affinity-diagnostics'],
              })
            }}
          />
          {tasks.isPending ? <LoadingState /> : null}
          {tasks.isError ? (
            <ErrorState onRetry={() => void tasks.refetch()} />
          ) : null}
          {!tasks.isError && tasks.data ? (
            <>
              <StaticDataTable
                data={tasks.data.items ?? []}
                getRowKey={(row) => row.id}
                emptyContent={t('No diagnostic tasks')}
                columns={[
                  {
                    id: 'user',
                    header: t('User ID'),
                    cell: (row) => formatNumber(row.user_id),
                  },
                  {
                    id: 'state',
                    header: t('Status'),
                    cell: (row) => (
                      <span>
                        {t(diagnosticLabels[row.status] ?? row.status)}{' '}
                        {row.reason
                          ? `· ${t(diagnosticLabels[row.reason] ?? row.reason)}`
                          : ''}
                      </span>
                    ),
                  },
                  {
                    id: 'requested',
                    header: t('Admitted sample quota'),
                    cell: (row) =>
                      `${formatNumber(row.requested_count)} / ${formatNumber(row.max_count)} · ${formatNumber(row.requested_bytes)} B`,
                  },
                  {
                    id: 'saved',
                    header: t('Saved samples'),
                    cell: (row) =>
                      `${formatNumber(row.saved_count)} · ${formatNumber(row.saved_bytes)} B`,
                  },
                  {
                    id: 'skipped',
                    header: t('Bodies not collected'),
                    cell: (row) => formatNumber(row.skipped_body),
                  },
                  {
                    id: 'dropped',
                    header: t('Dropped samples'),
                    cell: (row) => (
                      <span>
                        {formatNumber(row.dropped_count)}
                        {row.stats_incomplete
                          ? ` · ${t('Statistics incomplete')}`
                          : ''}
                        {row.cleanup_error ? ` · ${t('Cleanup backlog')}` : ''}
                      </span>
                    ),
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    cell: (row) => (
                      <div className='flex flex-wrap gap-2'>
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={
                            row.expires_at <= Date.now() ||
                            row.status === 'deleting'
                          }
                          onClick={() => setSelected(row)}
                        >
                          {t('Samples')}
                        </Button>
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={
                            !['running', 'starting'].includes(row.status)
                          }
                          onClick={() =>
                            setAction({ task: row, remove: false })
                          }
                        >
                          {t('Stop')}
                        </Button>
                        <Button
                          variant='destructive'
                          size='sm'
                          disabled={row.status === 'deleting'}
                          onClick={() => setAction({ task: row, remove: true })}
                        >
                          {t('Delete')}
                        </Button>
                      </div>
                    ),
                  },
                ]}
              />
              <DataTablePagination table={table} compact />
            </>
          ) : null}
          {current ? <SamplePanel key={current.id} task={current} /> : null}
          <ConfirmDialog
            open={action !== null}
            onOpenChange={(open) => {
              if (!open) setAction(null)
            }}
            title={
              action?.remove
                ? t('Delete diagnostic task')
                : t('Stop diagnostic task')
            }
            desc={
              action?.remove
                ? t(
                    'Delete all samples and stop capture. Storage is released only after physical cleanup.'
                  )
                : t(
                    'Stop new samples. Already admitted samples may still be saved.'
                  )
            }
            destructive={action?.remove}
            isLoading={mutation.isPending}
            handleConfirm={() => {
              if (action) mutation.mutate(action)
            }}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function CreateDiagnostic(props: {
  userId?: number
  onCreated: (task: DiagnosticTask) => void
}) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<DiagnosticFormValues | null>(null)
  const form = useForm<DiagnosticFormValues>({
    resolver: zodResolver(diagnosticSchema),
    defaultValues: {
      user_id: props.userId ?? 0,
      duration_seconds: 600,
      max_count: 100,
      max_body_bytes: 1048576,
      max_bytes: 20971520,
      capture_body: false,
      excluded_headers: '',
    },
  })
  const mutation = useMutation({
    mutationFn: createTask,
    onSuccess: (task) => {
      setPending(null)
      props.onCreated(task)
    },
  })
  const fields = [
    { name: 'user_id', label: t('User ID'), max: undefined },
    { name: 'duration_seconds', label: t('Duration (seconds)'), max: 600 },
    { name: 'max_count', label: t('Maximum samples'), max: 100 },
    { name: 'max_body_bytes', label: t('Maximum body bytes'), max: 1048576 },
    { name: 'max_bytes', label: t('Maximum task bytes'), max: 20971520 },
  ] as const
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(setPending)} className='space-y-4'>
        <div className='grid gap-4 sm:grid-cols-2 lg:grid-cols-3'>
          {fields.map((item) => (
            <FormField
              key={item.name}
              control={form.control}
              name={item.name}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{item.label}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='number'
                      min={item.name === 'max_bytes' ? 1024 : 1}
                      max={item.max}
                      onChange={(event) =>
                        field.onChange(event.target.valueAsNumber)
                      }
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}
        </div>
        <FormField
          control={form.control}
          name='excluded_headers'
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {t('Additional credential headers to exclude')}
              </FormLabel>
              <FormControl>
                <Input
                  {...field}
                  placeholder={t('Header names separated by commas')}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='capture_body'
          render={({ field }) => (
            <FormItem className='flex items-center gap-2'>
              <FormControl>
                <Checkbox
                  checked={field.value}
                  onCheckedChange={(checked) =>
                    field.onChange(checked === true)
                  }
                />
              </FormControl>
              <FormLabel>
                {t('Capture complete JSON bodies without redaction')}
              </FormLabel>
              <FormMessage />
            </FormItem>
          )}
        />
        <Button type='submit' disabled={mutation.isPending}>
          {t('Start diagnostic task')}
        </Button>
        <ConfirmDialog
          open={pending !== null}
          onOpenChange={(open) => {
            if (!open) setPending(null)
          }}
          title={t('Start diagnostic task')}
          desc={
            pending?.capture_body
              ? t(
                  'Complete JSON bodies may contain sensitive data. They are stored unchanged and are accessible to administrators for one hour from task creation.'
                )
              : t(
                  'Capture restricted headers and allowed context only. JSON bodies will not be collected.'
                )
          }
          isLoading={mutation.isPending}
          handleConfirm={() => {
            if (pending) mutation.mutate(pending)
          }}
        />
      </form>
    </Form>
  )
}

function SamplePanel(props: { task: DiagnosticTask }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])
  const expired =
    now >= props.task.expires_at || props.task.status === 'deleting'
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 20,
  })
  const [sample, setSample] = useState<number | null>(null)
  const samples = useQuery({
    queryKey: ['diagnostic-samples', props.task.id, pagination],
    queryFn: () =>
      listSamples(props.task.id, pagination.pageIndex + 1, pagination.pageSize),
    enabled: !expired,
    gcTime: 0,
    retry: false,
    refetchInterval: expired ? false : 2000,
  })
  const table = useReactTable({
    data: samples.data?.items ?? [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    rowCount: samples.data?.total ?? 0,
    state: { pagination },
    onPaginationChange: setPagination,
  })
  if (expired) {
    return (
      <Alert>
        <AlertDescription>
          {t('Diagnostic task is unavailable or expired')}
        </AlertDescription>
      </Alert>
    )
  }
  return (
    <section className='space-y-4' aria-label={t('Samples')}>
      <h2 className='text-lg font-semibold'>
        {t('Samples')} · {formatNumber(props.task.id)}
      </h2>
      <p className='text-muted-foreground text-sm'>
        {t('Queue drops')}: {formatNumber(props.task.queue_dropped)} ·{' '}
        {t('Write drops')}: {formatNumber(props.task.write_dropped)} ·{' '}
        {t('Admission drops')}: {formatNumber(props.task.admission_dropped)} ·{' '}
        {t('Maximum task bytes')}: {formatNumber(props.task.max_bytes)} B
      </p>
      {samples.isPending ? <LoadingState /> : null}
      {samples.isError ? (
        <ErrorState
          title={t('Diagnostic task is unavailable or expired')}
          onRetry={() => void samples.refetch()}
        />
      ) : null}
      {samples.data && !samples.isError ? (
        <>
          <StaticDataTable
            data={samples.data.items ?? []}
            getRowKey={(row) => row.id}
            emptyContent={t('No diagnostic samples')}
            columns={[
              {
                id: 'request',
                header: t('Request ID'),
                cell: (row) => row.request_id,
              },
              { id: 'model', header: t('Model'), cell: (row) => row.model },
              { id: 'path', header: t('Path'), cell: (row) => row.path },
              {
                id: 'body',
                header: t('Body status'),
                cell: (row) =>
                  `${t(diagnosticLabels[row.body_kind] ?? row.body_kind)} · ${t(diagnosticLabels[row.body_status] ?? row.body_status)}`,
              },
              {
                id: 'size',
                header: t('Size'),
                cell: (row) => `${formatNumber(row.size_bytes)} B`,
              },
              {
                id: 'view',
                header: t('Actions'),
                cell: (row) => (
                  <Button variant='outline' onClick={() => setSample(row.id)}>
                    {t('View sample')}
                  </Button>
                ),
              },
            ]}
          />
          <DataTablePagination table={table} compact />
        </>
      ) : null}
      {sample !== null && !samples.isError ? (
        <SampleDialog
          task={props.task.id}
          sample={sample}
          onClose={() => setSample(null)}
        />
      ) : null}
    </section>
  )
}

function SampleDialog(props: {
  task: number
  sample: number
  onClose: () => void
}) {
  const { t } = useTranslation()
  const detail = useQuery({
    queryKey: ['diagnostic-detail', props.task, props.sample],
    queryFn: () => getSample(props.task, props.sample),
    gcTime: 0,
    retry: false,
  })
  const form = useForm<ExtractionFormValues>({
    resolver: zodResolver(extractionSchema),
    defaultValues: { type: 'gjson', field: '' },
  })
  const extraction = useMutation({
    mutationFn: (values: ExtractionFormValues) =>
      extractSample(props.task, props.sample, values),
  })
  const download = useMutation({
    mutationFn: () => downloadSample(props.task, props.sample),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Diagnostic sample')}
      description={t(
        'HTTP bodies are captured at routing. Responses WebSocket bodies are normalized JSON, not original event frames.'
      )}
      contentClassName='sm:max-w-4xl'
    >
      <div className='space-y-4'>
        {detail.isPending ? <LoadingState /> : null}
        {detail.isError ? (
          <ErrorState title={t('Diagnostic task is unavailable or expired')} />
        ) : null}
        {detail.data && !detail.isError ? (
          <>
            <CodeBlock
              code={JSON.stringify(detail.data.metadata, null, 2)}
              language='json'
              title={t('Captured metadata')}
            />
            {detail.data.sample.body_status === 'captured' ? (
              <>
                <div className='flex gap-2'>
                  <Button
                    onClick={() => download.mutate()}
                    disabled={download.isPending}
                  >
                    {t('Download original body')}
                  </Button>
                  <CopyButton value={detail.data.body} />
                </div>
                <CodeBlock
                  code={detail.data.body}
                  language='json'
                  title={t('Captured JSON body')}
                />
              </>
            ) : (
              <Alert>
                <AlertDescription>
                  {t('Diagnostic body was not collected')}:{' '}
                  {t(
                    diagnosticLabels[detail.data.sample.body_status] ??
                      detail.data.sample.body_status
                  )}
                </AlertDescription>
              </Alert>
            )}
            <Form {...form}>
              <form
                onSubmit={form.handleSubmit((values) =>
                  extraction.mutate(values)
                )}
                className='space-y-3'
              >
                <FormField
                  control={form.control}
                  name='type'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Field source')}</FormLabel>
                      <FormControl>
                        <NativeSelect {...field}>
                          {[
                            'request_header',
                            'gjson',
                            'context_int',
                            'context_string',
                          ].map((type) => (
                            <NativeSelectOption key={type} value={type}>
                              {type}
                            </NativeSelectOption>
                          ))}
                        </NativeSelect>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='field'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Field name or JSON path')}</FormLabel>
                      <FormControl>
                        <Input {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <Button type='submit' disabled={extraction.isPending}>
                  {t('Extract field')}
                </Button>
              </form>
            </Form>
            {extraction.isError ? (
              <ErrorState title={t('Field extraction failed')} />
            ) : null}
            {extraction.data && !extraction.isError ? (
              <div role='status'>
                <p>
                  {t('Extraction status')}:{' '}
                  {t(
                    diagnosticLabels[extraction.data.status] ??
                      extraction.data.status
                  )}
                </p>
                <CodeBlock code={extraction.data.value} language='text' />
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'A stable value for one user does not prove global uniqueness.'
                  )}
                </p>
              </div>
            ) : null}
          </>
        ) : null}
      </div>
    </Dialog>
  )
}
