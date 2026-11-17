import { Await } from '../Await';
import { api } from '../api';
import type { Board as BoardData } from '../types';
import { PageHead, Section, Warnings } from '../ui';
import { useLoad } from '../useLoad';

export function Board() {
  const state = useLoad(() => api<BoardData>('GET', '/api/board'));
  return (
    <>
      <PageHead title="Board" />
      <Await state={state}>
        {(b) => (
          <>
            <Warnings warnings={b.warnings} />
            {b.untriaged > 0 && (
              <div className="note">
                {b.untriaged} untriaged issue{b.untriaged === 1 ? '' : 's'} hidden.{' '}
                <a href="#/triage">Open triage</a>
              </div>
            )}
            <Section title="Epics" issues={b.epics} />
            <Section title="Ready" issues={b.ready} empty="No ready work." />
            <Section title="In progress" issues={b.claimed} />
            <Section title="Blocked" issues={b.blocked} />
          </>
        )}
      </Await>
    </>
  );
}
