export type DiagnosticTask = {
  id: number
  user_id: number
  status: string
  reason: string
  started_at: number
  deadline: number
  expires_at: number
  capture_body: boolean
  max_count: number
  max_body_bytes: number
  max_bytes: number
  requested_count: number
  requested_bytes: number
  saved_count: number
  saved_bytes: number
  skipped_body: number
  dropped_count: number
  queue_dropped: number
  write_dropped: number
  admission_dropped: number
  stats_incomplete: boolean
  cleanup_error: boolean
}
export type DiagnosticSample = {
  id: number
  task_id: number
  captured_at: number
  request_id: string
  model: string
  path: string
  body_status: string
  body_kind: string
  size_bytes: number
}
export type SampleDetail = {
  sample: DiagnosticSample
  metadata: Record<string, unknown>
  body: string
}
export type Extraction = { status: string; value: string }
export type PageResult<T> = {
  items: T[] | null
  total: number
  redis_paused?: boolean
  node_name?: string
}
