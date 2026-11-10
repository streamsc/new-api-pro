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
export type ResourceLimit = { enabled: boolean; limit: number }
export type ResourceLimits = Record<
  'concurrency' | 'input_tpm' | 'rpm',
  ResourceLimit
>
export type ResourceUsage = Record<keyof ResourceLimits, number>
export type ResourceSources = Record<keyof ResourceLimits, string>
export type UserModelResource = {
  user_id: number
  model: string
  configured: boolean
  limits: ResourceLimits
  sources: ResourceSources
  usage?: ResourceUsage
  available: boolean
}
export type ResourceSnapshot = {
  limits: ResourceLimits
  sources?: ResourceSources
  usage?: ResourceUsage
  available: boolean
}
export const resourceSourceKeys: Record<string, string> = {
  user_override: 'User override',
  user_default: 'User default',
  model_default: 'Model default',
  unconfigured: 'Rule removed',
}
