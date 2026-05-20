import { useEffect, useRef, useState } from 'react';
import { Box, CircularProgress } from '@wso2/oxygen-ui';
import { useAuth } from './useAuth';

const CALLBACK_KEY = 'app_factory_auth_callback_pending';
const CALLBACK_TIMEOUT_MS = 60000;

export default function AuthGuard({ children }: { children: React.ReactNode }) {
  const { isSignedIn, isLoading, signIn } = useAuth();
  const signInTriggered = useRef(false);
  const [callbackPending, setCallbackPending] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    if (params.has('code') && params.has('state')) {
      sessionStorage.setItem(CALLBACK_KEY, Date.now().toString());
      return true;
    }
    const ts = sessionStorage.getItem(CALLBACK_KEY);
    if (ts && Date.now() - parseInt(ts, 10) < CALLBACK_TIMEOUT_MS) {
      return true;
    }
    sessionStorage.removeItem(CALLBACK_KEY);
    return false;
  });

  useEffect(() => {
    if (isSignedIn && callbackPending) {
      sessionStorage.removeItem(CALLBACK_KEY);
      setCallbackPending(false);
    }
  }, [isSignedIn, callbackPending]);

  useEffect(() => {
    if (!callbackPending) return;
    const timer = setTimeout(() => {
      sessionStorage.removeItem(CALLBACK_KEY);
      setCallbackPending(false);
    }, CALLBACK_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [callbackPending]);

  useEffect(() => {
    if (!isLoading && !isSignedIn && !signInTriggered.current && !callbackPending) {
      signInTriggered.current = true;
      signIn();
    }
  }, [isLoading, isSignedIn, signIn, callbackPending]);

  if (isLoading || callbackPending) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="100vh">
        <CircularProgress />
      </Box>
    );
  }

  if (!isSignedIn) {
    return null;
  }

  return <>{children}</>;
}
