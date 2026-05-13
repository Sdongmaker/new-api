import assert from 'node:assert/strict'
import { describe, test } from 'node:test'
import {
  buildCCSwitchURL,
  getCCSwitchServerAddress,
  normalizeCCSwitchApiKey,
} from './cc-switch-url'

describe('cc-switch url helpers', () => {
  test('normalizes api keys without duplicating the sk prefix', () => {
    assert.equal(normalizeCCSwitchApiKey('abc'), 'sk-abc')
    assert.equal(normalizeCCSwitchApiKey('sk-abc'), 'sk-abc')
  })

  test('builds codex import urls with a /v1 endpoint', () => {
    const url = buildCCSwitchURL(
      'codex',
      'My Codex',
      { model: 'gpt-5.4', reasoning_effort: 'high' },
      'sk-abc',
      'https://api.example.com'
    )

    assert.equal(
      url,
      'ccswitch://v1/import?resource=provider&app=codex&name=My+Codex&endpoint=https%3A%2F%2Fapi.example.com%2Fv1&apiKey=sk-abc&model=gpt-5.4&reasoning_effort=high&homepage=https%3A%2F%2Fapi.example.com&enabled=true'
    )
  })

  test('does not duplicate slashes before codex /v1 endpoint', () => {
    const url = buildCCSwitchURL(
      'codex',
      'My Codex',
      { model: 'gpt-5.4' },
      'sk-abc',
      'https://api.example.com/'
    )

    assert.equal(
      new URL(url).searchParams.get('endpoint'),
      'https://api.example.com/v1'
    )
  })

  test('falls back to the provided origin when status is missing or invalid', () => {
    const storage = {
      getItem(key: string) {
        if (key === 'status') return '{bad json'
        return null
      },
    } as Storage

    assert.equal(
      getCCSwitchServerAddress({
        localStorage: storage,
        fallbackOrigin: 'https://fallback.example.com',
      }),
      'https://fallback.example.com'
    )
  })

  test('reads the server address from local storage status when present', () => {
    const storage = {
      getItem(key: string) {
        if (key === 'status') {
          return JSON.stringify({ server_address: 'https://api.example.com' })
        }
        return null
      },
    } as Storage

    assert.equal(
      getCCSwitchServerAddress({
        localStorage: storage,
        fallbackOrigin: 'https://fallback.example.com',
      }),
      'https://api.example.com'
    )
  })
})
