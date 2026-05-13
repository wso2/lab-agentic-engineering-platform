import { env } from '../../config/env';
import { getToken } from './rest';

export interface BillingOrg {
  id?: string;
  name?: string;
  subscription?: unknown;
  [key: string]: unknown;
}

export async function fetchBillingOrg(product: string): Promise<BillingOrg> {
  const base = env.BILLING_API_BASE_URL;
  if (!base) throw new Error('BILLING_API_BASE_URL is not configured');

  const url = `${base}/api/v1/organization?product=${encodeURIComponent(product)}`;
  const headers: Record<string, string> = { Accept: 'application/json' };

  const token = await getToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  const res = await fetch(url, { headers });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`Billing API error ${res.status}: ${text}`);
  }
  return res.json() as Promise<BillingOrg>;
}
