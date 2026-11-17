import { Await } from '../Await';
import { api } from '../api';
import type { Primer as PrimerData } from '../types';
import { PageHead, Section, Warnings } from '../ui';
import { useLoad } from '../useLoad';

export function Primer() {
  const state = useLoad(() => api<PrimerData>('GET', '/api/primer'));
  return (
    <>
      <PageHead title="Primer" />
      <Await state={state}>
        {(p) => (
          <>
            <p className="lede">
              What an agent session would be shown. {p.openTotal} open, {p.untriaged} untriaged.
            </p>
            <Warnings warnings={p.warnings} />
            <Section title="Ready" issues={p.ready} />
            <Section title="In progress" issues={p.inProgress} />
            <Section title="Epics" issues={p.epics} />
            <h2>Conventions</h2>
            <pre>{p.conventions}</pre>
          </>
        )}
      </Await>
    </>
  );
}
