declare module 'swagger-ui-react' {
  import type { ComponentType } from 'react';
  interface SwaggerUIProps {
    spec?: unknown;
    url?: string;
    docExpansion?: 'list' | 'full' | 'none';
    deepLinking?: boolean;
    defaultModelsExpandDepth?: number;
    defaultModelExpandDepth?: number;
    displayOperationId?: boolean;
    displayRequestDuration?: boolean;
    filter?: boolean | string;
    showExtensions?: boolean;
    showCommonExtensions?: boolean;
    supportedSubmitMethods?: string[];
    tryItOutEnabled?: boolean;
    requestInterceptor?: (request: unknown) => unknown;
    responseInterceptor?: (response: unknown) => unknown;
    onComplete?: (system: unknown) => void;
    plugins?: unknown[];
    presets?: unknown[];
    layout?: string;
  }
  const SwaggerUI: ComponentType<SwaggerUIProps>;
  export default SwaggerUI;
}

declare module 'swagger-ui-react/swagger-ui.css';
