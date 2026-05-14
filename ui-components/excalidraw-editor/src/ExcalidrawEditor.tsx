import type React from 'react';
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
} from 'react';
import { Box, CircularProgress } from '@wso2/oxygen-ui';

// The Excalidraw bundle is large (~500KB gz) and only needed when a
// diagram file is actually opened, so it loads lazily inside Suspense.
// The CSS lives at a separate export and must be imported explicitly —
// without it the canvas grows to ~19M px because the .excalidraw root
// has no height constraints.
const ExcalidrawComponent = lazy(async () => {
  const [mod] = await Promise.all([
    import('@excalidraw/excalidraw'),
    import('@excalidraw/excalidraw/index.css'),
  ]);
  return { default: mod.Excalidraw };
});

export interface ExcalidrawEditorRef {
  getContent(): string;
  setContent(json: string): void;
}

export interface ExcalidrawEditorProps {
  /** Serialised Excalidraw scene JSON. Empty string seeds a blank canvas. */
  value: string;
  /** Fired (debounced ~150ms) with the latest serialised JSON. */
  onChange?: (json: string) => void;
  /** Imperative handle for programmatic content updates. */
  editorRef?: React.Ref<ExcalidrawEditorRef>;
  /** When true, the canvas is view-only. */
  readOnly?: boolean;
  /** Fill the parent's height. */
  fillHeight?: boolean;
}

// We treat Excalidraw scene contents as opaque JSON: persisted as strings,
// passed back to Excalidraw on rehydration without introspection. Local
// shapes use `any` to avoid coupling to the library's strict element/state
// types — the component is a pure shuttle between persistence and the
// canvas API.
/* eslint-disable @typescript-eslint/no-explicit-any */
type Scene = { elements?: any; appState?: any; files?: any };
type ExcalidrawAPI = {
  updateScene(payload: any): void;
  getSceneElements(): any;
  getAppState(): any;
  getFiles(): any;
};

// `appState.collaborators` is a Map at runtime; JSON-stringifying turns
// it into `{}` and JSON-parsing brings it back as a plain object, which
// crashes Excalidraw's iteration with "collaborators.forEach is not a
// function". Drop it on both sides — Excalidraw rebuilds it internally.
function sanitizeAppState(appState: any): any {
  if (!appState || typeof appState !== 'object') return appState;
  const { collaborators: _drop, ...rest } = appState;
  return rest;
}

function parseScene(value: string): Scene | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value);
    if (!parsed || typeof parsed !== 'object') return null;
    return {
      elements: parsed.elements,
      appState: sanitizeAppState(parsed.appState),
      files: parsed.files,
    };
  } catch {
    return null;
  }
}

function ExcalidrawEditorImpl({
  value,
  onChange,
  editorRef,
  readOnly,
  fillHeight,
}: ExcalidrawEditorProps) {
  const apiRef = useRef<ExcalidrawAPI | null>(null);
  const lastEmittedRef = useRef<string>(value);
  const debounceTimerRef = useRef<number | null>(null);

  // Mount-time snapshot of `value`. Subsequent prop updates flow through
  // the imperative setContent path (matches MdEditor's uncontrolled
  // pattern), so re-parsing on every render is unnecessary.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const initialData = useMemo(() => parseScene(value), []);

  useImperativeHandle(
    editorRef,
    () => ({
      getContent: () => {
        const api = apiRef.current;
        if (!api) return lastEmittedRef.current ?? '';
        const elements = api.getSceneElements();
        const appState = sanitizeAppState(api.getAppState());
        const files = api.getFiles();
        return JSON.stringify({ elements, appState, files });
      },
      setContent: (json: string) => {
        const api = apiRef.current;
        if (!api) return;
        const parsed = parseScene(json);
        api.updateScene({
          elements: parsed?.elements ?? [],
          appState: parsed?.appState ?? {},
        });
        lastEmittedRef.current = json;
      },
    }),
    [],
  );

  const handleChange = useCallback(
    (elements: any, appState: any, files: any) => {
      if (debounceTimerRef.current !== null) {
        window.clearTimeout(debounceTimerRef.current);
      }
      debounceTimerRef.current = window.setTimeout(() => {
        const next = JSON.stringify({
          elements,
          appState: sanitizeAppState(appState),
          files,
        });
        if (next === lastEmittedRef.current) return;
        lastEmittedRef.current = next;
        onChange?.(next);
      }, 150);
    },
    [onChange],
  );

  useEffect(
    () => () => {
      if (debounceTimerRef.current !== null) {
        window.clearTimeout(debounceTimerRef.current);
      }
    },
    [],
  );

  // Excalidraw measures its parent and stamps an inline height on its
  // own root div. If the chain to a definite height isn't airtight,
  // mobile-layout mode kicks in and the inline height balloons (we've
  // seen ~19M px in practice). The absolutely-positioned inner div
  // breaks any feedback loop between Excalidraw's measured height and
  // the parent's content-height — the inner div is sized purely by
  // its `inset: 0`, so Excalidraw's children can't push it.
  return (
    <Box
      sx={{
        flex: fillHeight ? 1 : undefined,
        height: fillHeight ? undefined : '600px',
        minHeight: 0,
        minWidth: 0,
        position: 'relative',
        width: '100%',
        overflow: 'hidden',
      }}
    >
      <Box
        sx={{
          position: 'absolute',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          width: '100%',
          height: '100%',
        }}
      >
        <ExcalidrawComponent
          initialData={initialData as any}
          onChange={handleChange}
          viewModeEnabled={readOnly}
          excalidrawAPI={(api: any) => {
            apiRef.current = api as ExcalidrawAPI;
          }}
        />
      </Box>
    </Box>
  );
}

export function ExcalidrawEditor(props: ExcalidrawEditorProps) {
  const fallback = (
    <Box
      sx={{
        flex: props.fillHeight ? 1 : undefined,
        height: props.fillHeight ? '100%' : '600px',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <CircularProgress size={28} />
    </Box>
  );

  return (
    <Suspense fallback={fallback}>
      <ExcalidrawEditorImpl {...props} />
    </Suspense>
  );
}
