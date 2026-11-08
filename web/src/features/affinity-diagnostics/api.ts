import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { DiagnosticFormValues, ExtractionFormValues } from './lib/schema'
import type {
  DiagnosticTask,
  DiagnosticSample,
  Extraction,
  PageResult,
  SampleDetail,
} from './types'

const base = '/api/affinity-diagnostics'
type Envelope<T> = { success: boolean; message: string; data: T }
export async function listTasks(
  page: number,
  size: number
): Promise<PageResult<DiagnosticTask>> {
  return requireServerSuccess(
    (
      await api.get<Envelope<PageResult<DiagnosticTask>>>(base, {
        params: { page, page_size: size },
      })
    ).data
  ).data
}
export async function createTask(
  values: DiagnosticFormValues
): Promise<DiagnosticTask> {
  return requireServerSuccess(
    (
      await api.post<Envelope<DiagnosticTask>>(base, {
        ...values,
        excluded_headers: values.excluded_headers
          .split(/[\s,]+/)
          .filter(Boolean),
      })
    ).data
  ).data
}
export async function changeTask(id: number, remove: boolean): Promise<void> {
  const response = remove
    ? await api.delete(`${base}/${id}`)
    : await api.post(`${base}/${id}/stop`)
  requireServerSuccess(response.data)
}
export async function listSamples(
  task: number,
  page: number,
  size: number
): Promise<PageResult<DiagnosticSample>> {
  return requireServerSuccess(
    (
      await api.get<Envelope<PageResult<DiagnosticSample>>>(
        `${base}/${task}/samples`,
        { params: { page, page_size: size } }
      )
    ).data
  ).data
}
export async function getSample(
  task: number,
  sample: number
): Promise<SampleDetail> {
  return requireServerSuccess(
    (await api.get<Envelope<SampleDetail>>(`${base}/${task}/samples/${sample}`))
      .data
  ).data
}
export async function extractSample(
  task: number,
  sample: number,
  values: ExtractionFormValues
): Promise<Extraction> {
  const source =
    values.type === 'gjson'
      ? { type: values.type, path: values.field }
      : { type: values.type, key: values.field }
  return requireServerSuccess(
    (
      await api.post<Envelope<Extraction>>(
        `${base}/${task}/samples/${sample}/extract`,
        source
      )
    ).data
  ).data
}
export async function downloadSample(
  task: number,
  sample: number
): Promise<void> {
  const response = await api.get<Blob>(`${base}/${task}/samples/${sample}`, {
    params: { download: 'body' },
    responseType: 'blob',
  })
  const url = URL.createObjectURL(response.data)
  const link = document.createElement('a')
  link.href = url
  link.download = `affinity-sample-${sample}.json`
  link.click()
  URL.revokeObjectURL(url)
}
