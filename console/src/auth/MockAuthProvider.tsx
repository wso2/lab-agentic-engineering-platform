import { createContext, useContext, useCallback, type ReactNode } from 'react';

const MOCK_USER = {
  sub: 'mock-admin-001',
  email: 'admin@appfactory.dev',
  name: 'Admin User',
  given_name: 'Admin',
  family_name: 'User',
  ouId: 'a5000000-0000-0000-0000-adminroot001',
  ouName: 'Admin',
  ouHandle: 'admin',
};

interface MockAuthValue {
  isSignedIn: true;
  isLoading: false;
  user: typeof MOCK_USER;
  signIn: () => void;
  signOut: () => void;
  clearSession: () => void;
  getAccessToken: () => Promise<string>;
  getDecodedIdToken: () => Promise<typeof MOCK_USER>;
}

const MockAuthContext = createContext<MockAuthValue | undefined>(undefined);

export function MockAuthProvider({ children }: { children: ReactNode }) {
  const signIn = useCallback(() => {
    // no-op in bypass mode
  }, []);
  const signOut = useCallback(() => {
    // no-op in bypass mode
  }, []);
  const clearSession = useCallback(() => {
    // no-op in bypass mode
  }, []);
  const getAccessToken = useCallback(async () => {
    return 'mock-access-token';
  }, []);
  const getDecodedIdToken = useCallback(async () => {
    return MOCK_USER;
  }, []);

  const value: MockAuthValue = {
    isSignedIn: true,
    isLoading: false,
    user: MOCK_USER,
    signIn,
    signOut,
    clearSession,
    getAccessToken,
    getDecodedIdToken,
  };

  return (
    <MockAuthContext.Provider value={value}>
      {children}
    </MockAuthContext.Provider>
  );
}

export function useMockAuthContext(): MockAuthValue {
  const ctx = useContext(MockAuthContext);
  if (!ctx) {
    throw new Error('useMockAuthContext must be used within MockAuthProvider');
  }
  return ctx;
}
