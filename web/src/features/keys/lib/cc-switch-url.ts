export function getCCSwitchServerAddress(options?: {
  localStorage?: Storage | null
  fallbackOrigin?: string
}): string {
  const storage =
    options?.localStorage ??
    (typeof window !== 'undefined' ? window.localStorage : null)
  const fallbackOrigin =
    options?.fallbackOrigin ??
    (typeof window !== 'undefined' ? window.location.origin : '')

  try {
    const raw = storage?.getItem('status')
    if (raw) {
      const status = JSON.parse(raw) as { server_address?: string }
      if (status.server_address) return status.server_address
    }
  } catch {
    /* empty */
  }
  return fallbackOrigin
}

export function normalizeCCSwitchApiKey(apiKey: string): string {
  if (apiKey.startsWith('sk-')) return apiKey
  return `sk-${apiKey}`
}

export function buildCCSwitchURL(
  app: string,
  name: string,
  models: Record<string, string>,
  apiKey: string,
  serverAddress: string
): string {
  const normalizedServerAddress = serverAddress.replace(/\/+$/, '')
  const endpoint =
    app === 'codex'
      ? `${normalizedServerAddress}/v1`
      : normalizedServerAddress
  const params = new URLSearchParams()
  params.set('resource', 'provider')
  params.set('app', app)
  params.set('name', name)
  params.set('endpoint', endpoint)
  params.set('apiKey', apiKey)
  for (const [k, v] of Object.entries(models)) {
    if (v) params.set(k, v)
  }
  params.set('homepage', normalizedServerAddress)
  params.set('enabled', 'true')
  return `ccswitch://v1/import?${params.toString()}`
}
