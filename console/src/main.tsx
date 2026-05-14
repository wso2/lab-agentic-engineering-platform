import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { AcrylicOrangeTheme, CssBaseline, OxygenUIThemeProvider } from '@wso2/oxygen-ui';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import AppAuthProvider from './auth/AuthProvider';
import { App } from './App';

// One process-wide QueryClient. Defaults match the design's polling
// model: tab-visibility gating via refetchIntervalInBackground:false on
// each polling hook; refetchOnWindowFocus auto-refreshes when the user
// returns to the tab; staleTime:0 so polling cadence drives freshness.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: true,
      refetchIntervalInBackground: false,
      staleTime: 0,
      retry: 1,
    },
  },
});

createRoot(document.getElementById('app')!).render(
  <StrictMode>
    <AppAuthProvider>
      <OxygenUIThemeProvider theme={AcrylicOrangeTheme}>
        <CssBaseline />
        <QueryClientProvider client={queryClient}>
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </QueryClientProvider>
      </OxygenUIThemeProvider>
    </AppAuthProvider>
  </StrictMode>
);
