import { useAsgardeo } from '@asgardeo/react';
import { useMockAuthContext } from './MockAuthProvider';
import { env } from '../config/env';

const useAuth = env.VITE_DEV_BYPASS_AUTH ? useMockAuthContext : useAsgardeo;

export { useAuth };
