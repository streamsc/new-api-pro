import { z } from 'zod'

export const diagnosticSchema = z.object({
  user_id: z.number().int().positive(),
  duration_seconds: z.number().int().min(1).max(600),
  max_count: z.number().int().min(1).max(100),
  max_body_bytes: z.number().int().min(1).max(1048576),
  max_bytes: z.number().int().min(1024).max(20971520),
  capture_body: z.boolean(),
  excluded_headers: z.string().max(4096),
})
export type DiagnosticFormValues = z.infer<typeof diagnosticSchema>
export const extractionSchema = z.object({
  type: z.enum(['request_header', 'gjson', 'context_int', 'context_string']),
  field: z.string().min(1).max(4096),
})
export type ExtractionFormValues = z.infer<typeof extractionSchema>
