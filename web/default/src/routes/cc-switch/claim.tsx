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
import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { CheckCircle2, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'
import { api } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { PasswordInput } from '@/components/password-input'
import { AuthLayout } from '@/features/auth/auth-layout'

const claimSearchSchema = z.object({
  ticket: z.string().optional().catch(''),
  redirect: z.string().optional().catch('/console/topup'),
})

type ClaimResponse = {
  success: boolean
  message?: string
  data?: {
    user: AuthUser
    redirect_path: string
    needs_profile_setup: boolean
  }
}

type SetupFormState = {
  username: string
  password: string
  confirmPassword: string
}

async function consumeClaimTicket(ticket: string): Promise<ClaimResponse> {
  const res = await api.post(
    '/api/bootstrap/cc-switch/claim',
    { ticket },
    {
      skipBusinessError: true,
      skipErrorHandler: true,
    } as Record<string, unknown>
  )
  return res.data as ClaimResponse
}

async function updateClaimedProfile(values: {
  username: string
  password: string
  userId: number
}): Promise<{ success: boolean; message?: string }> {
  const res = await api.put(
    '/api/user/self',
    {
      username: values.username,
      display_name: values.username,
      password: values.password,
    },
    {
      skipBusinessError: true,
      headers: {
        'New-Api-User': String(values.userId),
      },
    } as Record<string, unknown>
  )
  return res.data as { success: boolean; message?: string }
}

function normalizeRedirectPath(path?: string): string {
  if (!path || !path.startsWith('/') || path.startsWith('//')) {
    return '/console/topup'
  }
  if (
    path !== '/console/topup' &&
    !path.startsWith('/console/topup/') &&
    !path.startsWith('/console/topup?')
  ) {
    return '/console/topup'
  }
  return path
}

function rememberClaimedUser(user: AuthUser) {
  try {
    useAuthStore.getState().auth.setUser(user)
  } catch {
    useAuthStore.setState((state) => ({
      ...state,
      auth: {
        ...state.auth,
        user,
      },
    }))
  }
  try {
    window.localStorage.setItem('uid', String(user.id))
  } catch {
    /* empty */
  }
}

function redirectTo(path: string) {
  window.location.replace(normalizeRedirectPath(path))
}

function readClaimParams(
  fallbackTicket: string,
  fallbackRedirect: string
): { ticket: string; redirect: string } {
  if (typeof window === 'undefined') {
    return { ticket: fallbackTicket, redirect: fallbackRedirect }
  }

  const fragment = window.location.hash.startsWith('#')
    ? window.location.hash.slice(1)
    : ''
  if (fragment) {
    const params = new URLSearchParams(fragment)
    const ticket = (params.get('ticket') || '').trim()
    if (ticket) {
      return {
        ticket,
        redirect: normalizeRedirectPath(params.get('redirect') || fallbackRedirect),
      }
    }
  }

  return {
    ticket: fallbackTicket,
    redirect: fallbackRedirect,
  }
}

function getErrorMessage(err: unknown): string | undefined {
  if (!err || typeof err !== 'object' || !('response' in err)) {
    return undefined
  }
  const response = (err as { response?: { data?: { message?: unknown } } })
    .response
  return typeof response?.data?.message === 'string'
    ? response.data.message
    : undefined
}

function CCSwitchClaimPage() {
  const { t } = useTranslation()
  const search = Route.useSearch()
  const ticket = (search.ticket ?? '').trim()
  const fallbackRedirect = normalizeRedirectPath(search.redirect)
  const consumedRef = useRef(false)
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [redirectPath, setRedirectPath] = useState(fallbackRedirect)
  const [claimedUser, setClaimedUser] = useState<AuthUser | null>(null)
  const [form, setForm] = useState<SetupFormState>({
    username: '',
    password: '',
    confirmPassword: '',
  })

  const isReady = Boolean(claimedUser)
  const title = useMemo(() => {
    if (loading) return t('Signing in with CC Switch')
    if (error) return t('CC Switch sign-in failed')
    return t('Finish setting up your account')
  }, [error, loading, t])

  useEffect(() => {
    if (consumedRef.current) return
    consumedRef.current = true

    async function runClaim() {
      const { ticket: claimTicket, redirect: claimRedirect } = readClaimParams(
        ticket,
        fallbackRedirect
      )
      if (!claimTicket) {
        setError(t('Claim link is missing a ticket.'))
        setLoading(false)
        return
      }
      if (typeof window !== 'undefined') {
        window.history.replaceState(null, '', window.location.pathname)
      }
      try {
        const response = await consumeClaimTicket(claimTicket)
        if (!response.success || !response.data?.user) {
          setError(response.message || t('Claim link is invalid or expired.'))
          setLoading(false)
          return
        }
        const nextRedirect = normalizeRedirectPath(
          response.data.redirect_path || claimRedirect
        )
        rememberClaimedUser(response.data.user)
        setClaimedUser(response.data.user)
        setRedirectPath(nextRedirect)
        const claimedUsername = response.data.user.username || ''
        setForm((prev) => ({
          ...prev,
          username: claimedUsername.startsWith('ccs_') ? '' : claimedUsername,
        }))
        if (!response.data.needs_profile_setup) {
          redirectTo(nextRedirect)
          return
        }
      } catch (err) {
        const message = getErrorMessage(err)
        setError(message || t('Claim link is invalid or expired.'))
      } finally {
        setLoading(false)
      }
    }

    void runClaim()
  }, [fallbackRedirect, t, ticket])

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const username = form.username.trim()
    if (!username) {
      toast.error(t('Please enter a username'))
      return
    }
    if (username.length > 20) {
      toast.error(t('Username must be at most 20 characters long'))
      return
    }
    if (form.password.length < 8 || form.password.length > 20) {
      toast.error(t('Password must be 8-20 characters long'))
      return
    }
    if (form.password !== form.confirmPassword) {
      toast.error(t("Passwords don't match."))
      return
    }
    if (!claimedUser) {
      toast.error(t('Failed to update profile'))
      return
    }

    setSubmitting(true)
    try {
      const response = await updateClaimedProfile({
        username,
        password: form.password,
        userId: claimedUser.id,
      })
      if (!response.success) {
        toast.error(response.message || t('Failed to update profile'))
        return
      }
      if (claimedUser) {
        rememberClaimedUser({
          ...claimedUser,
          username,
          display_name: username,
        })
      }
      toast.success(t('Account ready. Opening top-up page.'))
      redirectTo(redirectPath)
    } catch (_err) {
      toast.error(t('Failed to update profile'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthLayout>
      <div className='w-full space-y-6'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold sm:text-left'>
            {title}
          </h2>
          <p className='text-muted-foreground text-sm sm:text-base'>
            {t(
              'This signs you into the same account that CC Switch created for your API key.'
            )}
          </p>
        </div>

        {loading && (
          <div className='flex min-h-32 items-center justify-center'>
            <Spinner className='size-6' />
          </div>
        )}

        {!loading && error && (
          <Alert variant='destructive'>
            <AlertTitle>{t('Unable to continue')}</AlertTitle>
            <AlertDescription>
              {error}{' '}
              {t('Open the top-up page from CC Switch again to get a fresh link.')}
            </AlertDescription>
          </Alert>
        )}

        {!loading && !error && isReady && (
          <form className='space-y-4' onSubmit={handleSubmit}>
            <Alert>
              <CheckCircle2 className='size-4' />
              <AlertTitle>{t('CC Switch account connected')}</AlertTitle>
              <AlertDescription>
                {t('Set a username and password so you can sign in later.')}
              </AlertDescription>
            </Alert>

            <div className='space-y-2'>
              <label className='text-sm font-medium' htmlFor='ccs-username'>
                {t('Username')}
              </label>
              <Input
                id='ccs-username'
                value={form.username}
                maxLength={20}
                autoComplete='username'
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    username: event.target.value,
                  }))
                }
              />
            </div>

            <div className='space-y-2'>
              <label className='text-sm font-medium' htmlFor='ccs-password'>
                {t('Password')}
              </label>
              <PasswordInput
                id='ccs-password'
                value={form.password}
                maxLength={20}
                autoComplete='new-password'
                placeholder={t('8-20 characters')}
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    password: event.target.value,
                  }))
                }
              />
            </div>

            <div className='space-y-2'>
              <label
                className='text-sm font-medium'
                htmlFor='ccs-confirm-password'
              >
                {t('Confirm Password')}
              </label>
              <PasswordInput
                id='ccs-confirm-password'
                value={form.confirmPassword}
                maxLength={20}
                autoComplete='new-password'
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    confirmPassword: event.target.value,
                  }))
                }
              />
            </div>

            <Button className='w-full' disabled={submitting} type='submit'>
              {submitting ? (
                <>
                  <Spinner />
                  {t('Saving...')}
                </>
              ) : (
                <>
                  <ExternalLink />
                  {t('Continue to top-up')}
                </>
              )}
            </Button>
          </form>
        )}
      </div>
    </AuthLayout>
  )
}

export const Route = createFileRoute('/cc-switch/claim')({
  component: CCSwitchClaimPage,
  validateSearch: claimSearchSchema,
})
