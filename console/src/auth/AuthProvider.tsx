import { AsgardeoProvider } from '@asgardeo/react';
import { MockAuthProvider } from './MockAuthProvider';
import { env } from '../config/env';

const asgardeoConfig = {
  baseUrl: env.VITE_THUNDER_URL,
  clientId: env.VITE_THUNDER_CLIENT_ID,
  signInUrl: `${env.VITE_THUNDER_URL}/gate`,
  afterSignInUrl: env.VITE_SIGN_IN_REDIRECT_URL,
  afterSignOutUrl: env.VITE_SIGN_OUT_REDIRECT_URL,
  scopes: env.VITE_THUNDER_SCOPES.split(' '),
  storage: 'localStorage' as const,
  platform: 'IdentityServer' as const,
  tokenValidation: {
    idToken: {
      validate: false,
    },
  },
};

export default function AppAuthProvider({ children }: { children: React.ReactNode }) {
  if (env.VITE_DEV_BYPASS_AUTH) {
    return <MockAuthProvider>{children}</MockAuthProvider>;
  }

  return (
    <AsgardeoProvider {...asgardeoConfig}>
      {children}
    </AsgardeoProvider>
  );
}
