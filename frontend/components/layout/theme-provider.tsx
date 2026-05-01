'use client';

import * as React from 'react';
import { ThemeProvider as NextThemesProvider } from 'next-themes';

export type AccentTheme = 'violet' | 'blue' | 'teal' | 'green' | 'amber' | 'crimson';

type AccentOption = {
  value: AccentTheme;
  label: string;
  preview: string;
};

const ACCENT_STORAGE_KEY = 'nation2api-admin-accent';
const DEFAULT_ACCENT_THEME: AccentTheme = 'violet';

export const ACCENT_THEME_OPTIONS: AccentOption[] = [
  { value: 'violet', label: '紫罗兰', preview: '#8b5cf6' },
  { value: 'blue', label: '深海蓝', preview: '#2563eb' },
  { value: 'teal', label: '青碧', preview: '#0f766e' },
  { value: 'green', label: '森绿', preview: '#16a34a' },
  { value: 'amber', label: '琥珀', preview: '#d97706' },
  { value: 'crimson', label: '绳红', preview: '#dc2626' },
];

type AccentThemeContextValue = {
  accentTheme: AccentTheme;
  setAccentTheme: (next: AccentTheme) => void;
  options: AccentOption[];
};

const AccentThemeContext = React.createContext<AccentThemeContextValue | null>(null);

function isAccentTheme(value: string): value is AccentTheme {
  return ACCENT_THEME_OPTIONS.some((option) => option.value === value);
}

function readStoredAccentTheme(): AccentTheme {
  if (typeof window === 'undefined') return DEFAULT_ACCENT_THEME;
  const raw = window.localStorage.getItem(ACCENT_STORAGE_KEY);
  return raw && isAccentTheme(raw) ? raw : DEFAULT_ACCENT_THEME;
}

function readDocumentAccentTheme(): AccentTheme {
  if (typeof document === 'undefined') return DEFAULT_ACCENT_THEME;
  const raw = document.documentElement.dataset.accent;
  return raw && isAccentTheme(raw) ? raw : DEFAULT_ACCENT_THEME;
}

function applyAccentTheme(next: AccentTheme) {
  if (typeof window === 'undefined' || typeof document === 'undefined') return;
  const root = document.documentElement;
  if (root.dataset.accent === next) {
    window.localStorage.setItem(ACCENT_STORAGE_KEY, next);
    return;
  }
  root.dataset.accent = next;
  window.localStorage.setItem(ACCENT_STORAGE_KEY, next);
}

function AccentThemeProvider({ children }: { children: React.ReactNode }) {
  const [accentTheme, setAccentThemeState] = React.useState<AccentTheme>(DEFAULT_ACCENT_THEME);

  React.useLayoutEffect(() => {
    const resolved = readStoredAccentTheme();
    setAccentThemeState((current) => (current === resolved ? current : resolved));
    applyAccentTheme(resolved);
  }, []);

  React.useLayoutEffect(() => {
    applyAccentTheme(accentTheme);
  }, [accentTheme]);

  React.useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== ACCENT_STORAGE_KEY || !event.newValue || !isAccentTheme(event.newValue)) return;
      setAccentThemeState(event.newValue);
    };
    const observer = new MutationObserver(() => {
      const next = readDocumentAccentTheme();
      setAccentThemeState((current) => (current === next ? current : next));
    });
    window.addEventListener('storage', onStorage);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-accent'],
    });
    return () => {
      window.removeEventListener('storage', onStorage);
      observer.disconnect();
    };
  }, []);

  const setAccentTheme = React.useCallback((next: AccentTheme) => {
    setAccentThemeState(next);
  }, []);

  const value = React.useMemo<AccentThemeContextValue>(
    () => ({
      accentTheme,
      setAccentTheme,
      options: ACCENT_THEME_OPTIONS,
    }),
    [accentTheme, setAccentTheme],
  );

  return <AccentThemeContext.Provider value={value}>{children}</AccentThemeContext.Provider>;
}

export function useAccentTheme() {
  const context = React.useContext(AccentThemeContext);
  if (!context) {
    throw new Error('useAccentTheme must be used within ThemeProvider');
  }
  return context;
}

export function ThemeProvider({
  children,
  ...props
}: React.ComponentProps<typeof NextThemesProvider>) {
  return (
    <NextThemesProvider {...props}>
      <AccentThemeProvider>{children}</AccentThemeProvider>
    </NextThemesProvider>
  );
}
