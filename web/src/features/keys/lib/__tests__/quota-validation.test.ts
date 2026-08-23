/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { TFunction } from 'i18next'
import { describe, expect, test } from 'vitest'

import {
  getApiKeyFormDefaultValues,
  getApiKeyFormSchema,
} from '../api-key-form'

const t = ((key: string) => key) as TFunction

describe('API key quota validation', () => {
  test('rejects a limited quota that exceeds the backend storage limit', () => {
    const result = getApiKeyFormSchema(t).safeParse({
      ...getApiKeyFormDefaultValues(false),
      name: 'oversized quota',
      unlimited_quota: false,
      remain_quota_dollars: 10_000,
    })

    expect(result.success).toBe(false)
    if (result.success) return
    expect(result.error.issues).toContainEqual(
      expect.objectContaining({
        path: ['remain_quota_dollars'],
        message: 'Quota exceeds the maximum allowed amount',
      })
    )
  })
})
