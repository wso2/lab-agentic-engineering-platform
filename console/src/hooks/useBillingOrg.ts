import { useEffect, useState } from 'react';
import { fetchBillingOrg } from '../services/api/billing';
import type { BillingOrg } from '../services/api/billing';

export function useBillingOrg(product: string, enabled: boolean = true): { org: BillingOrg | null; isLoading: boolean } {
  const [org, setOrg] = useState<BillingOrg | null>(null);
  const [isLoading, setIsLoading] = useState<boolean>(enabled);

  useEffect(() => {
    if (!enabled) {
      setIsLoading(false);
      return;
    }
    let cancelled = false;
    setIsLoading(true);
    fetchBillingOrg(product)
      .then((result) => { if (!cancelled) setOrg(result); })
      .catch((err) => { console.warn('Failed to load billing org:', err); })
      .finally(() => { if (!cancelled) setIsLoading(false); });
    return () => { cancelled = true; };
  }, [product, enabled]);

  return { org, isLoading };
}
