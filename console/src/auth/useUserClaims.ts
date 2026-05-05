import { useEffect, useRef, useState } from 'react';
import { useAuth } from './useAuth';

export interface UserClaims {
  sub?: string;
  name?: string;
  email?: string;
  ouId?: string;
  ouName?: string;
  ouHandle?: string;
  [key: string]: unknown;
}

const OU_CLAIM_KEYS = ['ouId', 'ouName', 'ouHandle'] as const;

function hasOuClaims(claims: Record<string, unknown>): boolean {
  return OU_CLAIM_KEYS.some((key) => claims[key] != null && claims[key] !== '');
}

/**
 * Extracts user claims from the ID token and access token.
 * Organization unit claims (ouId, ouName, ouHandle) may be in either token.
 */
export function useUserClaims(): { claims: UserClaims | null; isLoading: boolean } {
  const { isSignedIn, getDecodedIdToken, getAccessToken } = useAuth();
  const [claims, setClaims] = useState<UserClaims | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const resolvedRef = useRef(false);

  useEffect(() => {
    if (!isSignedIn || resolvedRef.current) {
      setIsLoading(false);
      return;
    }

    let cancelled = false;

    const resolve = async () => {
      try {
        const idToken = await getDecodedIdToken();
        const idClaims = (idToken ?? {}) as Record<string, unknown>;

        if (hasOuClaims(idClaims)) {
          if (!cancelled) {
            setClaims(idClaims as UserClaims);
            resolvedRef.current = true;
          }
          return;
        }

        // Fall back to access token for org claims
        const accessToken = await getAccessToken();
        if (accessToken) {
          const parts = accessToken.split('.');
          if (parts.length === 3) {
            const payload = parts[1].replace(/-/g, '+').replace(/_/g, '/');
            const padded = payload + '='.repeat((4 - (payload.length % 4)) % 4);
            const decoded = JSON.parse(atob(padded)) as Record<string, unknown>;

            const merged = { ...idClaims };
            for (const key of OU_CLAIM_KEYS) {
              if (decoded[key] != null) {
                merged[key] = decoded[key];
              }
            }

            if (!cancelled) {
              setClaims(merged as UserClaims);
              resolvedRef.current = true;
            }
            return;
          }
        }

        if (!cancelled) {
          setClaims(idClaims as UserClaims);
          resolvedRef.current = true;
        }
      } catch {
        if (!cancelled) {
          setClaims(null);
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    };

    resolve();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isSignedIn]);

  return { claims, isLoading };
}
