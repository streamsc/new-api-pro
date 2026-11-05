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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ErrorState } from '@/components/error-state'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { LoadingState } from '@/components/loading-state'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { userResourcesConfigSchema } from './user-resources-schema'
import { UserResourcesVisualEditor } from './user-resources-visual-editor'

const formSchema = z.object({
  config: z.string().refine((value) => {
    try {
      return userResourcesConfigSchema.safeParse(JSON.parse(value)).success
    } catch {
      return false
    }
  }, 'Invalid resource limits configuration'),
  reason: z.string().trim().min(1).max(1000),
})

type FormValues = z.infer<typeof formSchema>
export function UserResourcesSection() {
  const { t } = useTranslation()
  const [editMode, setEditMode] = useState('visual')
  const client = useQueryClient()
  const query = useQuery({
    queryKey: ['user-resources-config'],
    queryFn: async () =>
      requireServerSuccess(
        (
          await api.get<{
            success: boolean
            data: z.infer<typeof userResourcesConfigSchema>
          }>('/api/option/user_resources')
        ).data
      ).data,
  })
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { config: '', reason: '' },
  })
  useEffect(() => {
    if (query.data) {
      if (!form.formState.isDirty) {
        form.reset({ config: JSON.stringify(query.data, null, 2), reason: '' })
        if (!userResourcesConfigSchema.safeParse(query.data).success) {
          setEditMode('json')
        }
      }
    }
  }, [query.data, form])
  const mutation = useMutation({
    mutationFn: async (values: FormValues) =>
      requireServerSuccess(
        (
          await api.put('/api/option/user_resources', {
            config: JSON.parse(values.config),
            reason: values.reason,
          })
        ).data
      ),
    onSuccess: async (_data, values) => {
      form.reset({ config: values.config, reason: '' })
      await client.invalidateQueries({ queryKey: ['user-resources-config'] })
      await client.invalidateQueries({ queryKey: ['self-resources'] })
    },
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        onRetry={() => {
          void query.refetch()
        }}
      />
    )
  }
  return (
    <SettingsSection title={t('User resource limits')}>
      <Form {...form}>
        <SettingsForm
          onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
        >
          <SettingsPageFormActions
            onSave={form.handleSubmit((values) => mutation.mutate(values))}
            isSaving={mutation.isPending}
          />
          <FormField
            control={form.control}
            name='config'
            render={({ field }) => (
              <FormItem data-settings-form-span='full'>
                <FormLabel>{t('Defaults and user overrides')}</FormLabel>
                <FormDescription>
                  {t(
                    'User overrides inherit default limits unless changed. Changes preserve usage counters and sync to other nodes within the configured interval (60 seconds by default).'
                  )}
                </FormDescription>
                <Tabs
                  className='min-w-0'
                  value={editMode}
                  onValueChange={(value) => {
                    if (value === 'visual') {
                      try {
                        userResourcesConfigSchema.parse(JSON.parse(field.value))
                      } catch {
                        form.setError('config', {
                          message: 'Invalid resource limits configuration',
                        })
                        return
                      }
                    }
                    form.clearErrors('config')
                    setEditMode(String(value))
                  }}
                >
                  <TabsList className='grid w-full grid-cols-2'>
                    <TabsTrigger value='visual'>{t('Visual')}</TabsTrigger>
                    <TabsTrigger value='json'>{t('JSON')}</TabsTrigger>
                  </TabsList>
                  <TabsContent value='visual' className='mt-4 min-w-0'>
                    {editMode === 'visual' && field.value && (
                      <UserResourcesVisualEditor
                        config={JSON.parse(field.value)}
                        onChange={(config) =>
                          field.onChange(JSON.stringify(config, null, 2))
                        }
                      />
                    )}
                  </TabsContent>
                  <TabsContent value='json' className='mt-4 min-w-0'>
                    <FormControl>
                      <JsonCodeEditor
                        value={field.value}
                        onChange={field.onChange}
                        ariaLabel={t('Defaults and user overrides')}
                        onBlur={field.onBlur}
                        textareaRef={field.ref}
                      />
                    </FormControl>
                  </TabsContent>
                </Tabs>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='reason'
            render={({ field }) => (
              <FormItem data-settings-form-span='full'>
                <FormLabel>{t('Reason for change')}</FormLabel>
                <FormControl>
                  <Input {...field} maxLength={1000} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
