import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { ACCESS_TOKEN_KEY, REFRESH_TOKEN_KEY, USERNAME_KEY } from '../api/client'

// Only the network-hitting exports are mocked; everything else (the
// localStorage key constants, clearTokens, setUnauthorizedListener) is the
// real implementation, since AuthContext depends on those directly.
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ...actual,
    login: vi.fn(),
    register: vi.fn(),
  }
})

import * as api from '../api/client'
import { AuthProvider, useAuth } from './AuthContext'

const mockedLogin = vi.mocked(api.login)
const mockedRegister = vi.mocked(api.register)

function wrapper({ children }: { children: ReactNode }) {
  return <AuthProvider>{children}</AuthProvider>
}

describe('AuthContext', () => {
  beforeEach(() => {
    localStorage.clear()
    mockedLogin.mockReset()
    mockedRegister.mockReset()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('starts with a null user when localStorage is empty', () => {
    const { result } = renderHook(() => useAuth(), { wrapper })
    expect(result.current.user).toBeNull()
  })

  it('login() populates user state and stores tokens under the real key names', async () => {
    mockedLogin.mockResolvedValue({ accessToken: 'access-1', refreshToken: 'refresh-1' })

    const { result } = renderHook(() => useAuth(), { wrapper })

    await act(async () => {
      await result.current.login('alice', 'pw')
    })

    expect(result.current.user).toEqual({
      username: 'alice',
      accessToken: 'access-1',
      refreshToken: 'refresh-1',
    })
    expect(localStorage.getItem(ACCESS_TOKEN_KEY)).toBe('access-1')
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-1')
    expect(localStorage.getItem(USERNAME_KEY)).toBe('alice')
  })

  it('register() calls the register endpoint then login(), with the same credentials, in that order', async () => {
    const callOrder: string[] = []
    mockedRegister.mockImplementation(async (username: string) => {
      callOrder.push('register')
      return { id: 1, username }
    })
    mockedLogin.mockImplementation(async () => {
      callOrder.push('login')
      return { accessToken: 'a', refreshToken: 'r' }
    })

    const { result } = renderHook(() => useAuth(), { wrapper })

    await act(async () => {
      await result.current.register('bob', 'pw')
    })

    expect(callOrder).toEqual(['register', 'login'])
    expect(mockedRegister).toHaveBeenCalledWith('bob', 'pw')
    expect(mockedLogin).toHaveBeenCalledWith('bob', 'pw')
  })

  it('logout() clears both in-memory user state and localStorage', async () => {
    mockedLogin.mockResolvedValue({ accessToken: 'access-1', refreshToken: 'refresh-1' })

    const { result } = renderHook(() => useAuth(), { wrapper })

    await act(async () => {
      await result.current.login('alice', 'pw')
    })
    expect(result.current.user).not.toBeNull()

    act(() => {
      result.current.logout()
    })

    expect(result.current.user).toBeNull()
    expect(localStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull()
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull()
    expect(localStorage.getItem(USERNAME_KEY)).toBeNull()
  })

  it('hydrates user state from localStorage tokens already present at mount, without a fresh login call', () => {
    localStorage.setItem(ACCESS_TOKEN_KEY, 'existing-access')
    localStorage.setItem(REFRESH_TOKEN_KEY, 'existing-refresh')
    localStorage.setItem(USERNAME_KEY, 'carol')

    const { result } = renderHook(() => useAuth(), { wrapper })

    expect(result.current.user).toEqual({
      username: 'carol',
      accessToken: 'existing-access',
      refreshToken: 'existing-refresh',
    })
    expect(mockedLogin).not.toHaveBeenCalled()
  })
})
