import { createFileRoute, redirect } from '@tanstack/react-router'
import { z } from 'zod'

import { AffinityDiagnostics } from '@/features/affinity-diagnostics'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/affinity-diagnostics')({
  beforeLoad: () => {
    if ((useAuthStore.getState().auth.user?.role ?? 0) < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  validateSearch: z.object({
    user_id: z.number().int().positive().optional().catch(undefined),
  }),
  component: DiagnosticRoute,
})
function DiagnosticRoute() {
  const search = Route.useSearch()
  return <AffinityDiagnostics userId={search.user_id} />
}
