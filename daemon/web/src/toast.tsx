import { createContext, useCallback, useContext, useState, type ReactNode } from 'react';
import { explain } from './api';

type Kind = 'ok' | 'bad';
interface ToastItem {
  id: number;
  msg: string;
  kind: Kind;
}

const Push = createContext<(msg: string, kind?: Kind) => void>(() => {});
let nextId = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const push = useCallback((msg: string, kind: Kind = 'ok') => {
    const id = ++nextId;
    setItems((cur) => [...cur, { id, msg, kind }]);
    // Errors stay longer: they are the ones a person needs to read.
    setTimeout(
      () => setItems((cur) => cur.filter((t) => t.id !== id)),
      kind === 'bad' ? 7000 : 3500,
    );
  }, []);
  return (
    <Push.Provider value={push}>
      {children}
      <div id="toasts">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`} role="status">
            {t.msg}
          </div>
        ))}
      </div>
    </Push.Provider>
  );
}

export const useToast = () => useContext(Push);

/** Wraps a call so a failure becomes an explained toast and a success a note. */
export function useAct() {
  const toast = useToast();
  return useCallback(
    async <T,>(fn: () => Promise<T>, success?: string): Promise<T | null> => {
      try {
        const result = await fn();
        if (success) toast(success, 'ok');
        return result;
      } catch (e) {
        toast(explain(e), 'bad');
        return null;
      }
    },
    [toast],
  );
}
