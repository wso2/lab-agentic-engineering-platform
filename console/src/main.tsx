import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { AcrylicOrangeTheme, CssBaseline, OxygenUIThemeProvider } from '@wso2/oxygen-ui';
import AppAuthProvider from './auth/AuthProvider';
import { App } from './App';

createRoot(document.getElementById('app')!).render(
  <StrictMode>
    <AppAuthProvider>
      <OxygenUIThemeProvider theme={AcrylicOrangeTheme}>
        <CssBaseline />
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </OxygenUIThemeProvider>
    </AppAuthProvider>
  </StrictMode>
);
