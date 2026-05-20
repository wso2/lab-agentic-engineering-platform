interface RuntimeEnv {
  VITE_CORE_API_BASE_URL?: string;
  VITE_THUNDER_URL?: string;
  VITE_THUNDER_CLIENT_ID?: string;
  VITE_THUNDER_SCOPES?: string;
  VITE_SIGN_IN_REDIRECT_URL?: string;
  VITE_SIGN_OUT_REDIRECT_URL?: string;
  VITE_DEV_BYPASS_AUTH?: string;
  BILLING_API_BASE_URL?: string;
}

declare global {
  interface Window {
    _env_?: RuntimeEnv;
  }
}

function getEnv(key: keyof RuntimeEnv): string | undefined {
  if (typeof window !== 'undefined' && window._env_) {
    const runtimeValue = window._env_[key];
    if (runtimeValue !== undefined && runtimeValue !== '') {
      return runtimeValue;
    }
  }
  return import.meta.env[key];
}

export const env = {
  VITE_CORE_API_BASE_URL: getEnv('VITE_CORE_API_BASE_URL') || '/asdlc-api-service',
  VITE_THUNDER_URL: getEnv('VITE_THUNDER_URL') || '',
  VITE_THUNDER_CLIENT_ID: getEnv('VITE_THUNDER_CLIENT_ID') || '',
  VITE_THUNDER_SCOPES: getEnv('VITE_THUNDER_SCOPES') || 'openid profile email',
  VITE_SIGN_IN_REDIRECT_URL: getEnv('VITE_SIGN_IN_REDIRECT_URL') || undefined,
  VITE_SIGN_OUT_REDIRECT_URL: getEnv('VITE_SIGN_OUT_REDIRECT_URL') || undefined,
  VITE_DEV_BYPASS_AUTH: getEnv('VITE_DEV_BYPASS_AUTH') === 'true',
  BILLING_API_BASE_URL: getEnv('BILLING_API_BASE_URL') || '',
} as const;
