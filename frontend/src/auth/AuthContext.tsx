import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import * as api from '../api/client'
import {
  ACCESS_TOKEN_KEY,
  REFRESH_TOKEN_KEY,
  USERNAME_KEY,
  setUnauthorizedListener,
} from '../api/client'

/**
 * Shared localStorage keys — see src/api/client.ts, which is the other
 * reader/writer of these same keys (it reads them directly, without going
 * through this context, to keep the fetch wrapper usable from plain module
 * code). Both files must keep the key names in sync.
 */
export interface AuthUser {
  username: string
  accessToken: string
  refreshToken: string
}

export interface AuthContextValue {
  user: AuthUser | null
  login: (username: string, password: string) => Promise<void>
  register: (username: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

function readStoredUser(): AuthUser | null {
  const accessToken = localStorage.getItem(ACCESS_TOKEN_KEY)
  const refreshToken = localStorage.getItem(REFRESH_TOKEN_KEY)
  const username = localStorage.getItem(USERNAME_KEY)
  if (!accessToken || !refreshToken || !username) return null
  return { username, accessToken, refreshToken }
}

function storeUser(user: AuthUser): void {
  localStorage.setItem(ACCESS_TOKEN_KEY, user.accessToken)
  localStorage.setItem(REFRESH_TOKEN_KEY, user.refreshToken)
  localStorage.setItem(USERNAME_KEY, user.username)
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(() => readStoredUser())

  // If the api client ever has to clear tokens on our behalf (a 401 that
  // survived a refresh attempt), reflect that immediately in state instead
  // of waiting for a reload.
  useEffect(() => {
    setUnauthorizedListener(() => setUser(null))
    return () => setUnauthorizedListener(null)
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const { accessToken, refreshToken } = await api.login(username, password)
    const nextUser: AuthUser = { username, accessToken, refreshToken }
    storeUser(nextUser)
    setUser(nextUser)
  }, [])

  const register = useCallback(
    async (username: string, password: string) => {
      await api.register(username, password)
      await login(username, password)
    },
    [login],
  )

  const logout = useCallback(() => {
    api.clearTokens()
    setUser(null)
  }, [])

  const value = useMemo<AuthContextValue>(
    () => ({ user, login, register, logout }),
    [user, login, register, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return ctx
}
