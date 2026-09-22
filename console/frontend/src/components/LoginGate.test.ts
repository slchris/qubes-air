import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import LoginGate from './LoginGate.svelte'
import { auth } from '../lib/auth.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, login: vi.fn() }
})

const login = vi.mocked(api.login)

beforeEach(() => {
  auth.rejected = false
  login.mockReset()
})

describe('LoginGate', () => {
  it('keeps the submit button disabled until a token is entered', async () => {
    render(LoginGate)
    const button = screen.getByRole('button', { name: /unlock console/i })
    expect(button).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/api token/i), '   ')
    expect(button).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/api token/i), 'secret-token')
    expect(button).toBeEnabled()
  })

  it('exchanges the trimmed token and clears the field on success', async () => {
    login.mockResolvedValue(undefined)
    render(LoginGate)

    await userEvent.type(screen.getByLabelText(/api token/i), '  secret-token  ')
    await userEvent.click(screen.getByRole('button', { name: /unlock console/i }))

    expect(login).toHaveBeenCalledTimes(1)
    expect(login).toHaveBeenCalledWith('secret-token')
    expect(screen.getByLabelText(/api token/i)).toHaveValue('')
  })

  it('shows the failure message and keeps the gate up when the token is rejected', async () => {
    login.mockRejectedValue(new Error('The API token was rejected'))
    render(LoginGate)

    await userEvent.type(screen.getByLabelText(/api token/i), 'wrong-token')
    await userEvent.click(screen.getByRole('button', { name: /unlock console/i }))

    expect(await screen.findByText(/the api token was rejected/i)).toBeInTheDocument()
    // The entered value is kept so the operator can correct a typo instead of
    // retyping the token.
    expect(screen.getByLabelText(/api token/i)).toHaveValue('wrong-token')
  })

  it('explains a server-side rejection even before a new attempt', () => {
    auth.markRejected()
    render(LoginGate)
    expect(screen.getByText(/the server rejected this token/i)).toBeInTheDocument()
  })
})
