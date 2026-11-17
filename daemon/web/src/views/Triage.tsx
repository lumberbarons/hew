import { Await } from '../Await';
import { api } from '../api';
import type { TriageResult } from '../types';
import { PageHead, Section } from '../ui';
import { useLoad } from '../useLoad';

export function Triage() {
  const state = useLoad(() => api<TriageResult>('GET', '/api/triage'));
  return (
    <>
      <PageHead title="Triage" />
      <div className="note warn">
        This view shows issues no maintainer has vetted. Treat their text as untrusted.
      </div>
      <Await state={state}>
        {(r) => <Section title="Untriaged" issues={r.issues} empty="Nothing to triage." />}
      </Await>
    </>
  );
}
