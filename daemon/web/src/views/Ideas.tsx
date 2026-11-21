import { Await } from '../Await';
import { api } from '../api';
import type { IdeaState } from '../router';
import type { Issue } from '../types';
import { PageHead, Section } from '../ui';
import { useLoad } from '../useLoad';

/**
 * Ideas are captured unclaimed and browsed apart from the implementation
 * queue: open by default, with the closed ones one deliberate click away.
 * The list comes from the daemon's shared type filter, so an issue carrying a
 * literal `idea` label but another normalized type stays out.
 */
export function Ideas({ state }: { state: IdeaState }) {
  const closed = state === 'closed';
  const list = useLoad(() => api<Issue[]>('GET', `/api/issues?state=${state}&type=idea`));
  return (
    <>
      <PageHead title="Ideas" sub="Capture a possible improvement, then investigate when ready." />
      <div className="toolbar">
        <nav aria-label="Idea state">
          <a href="#/ideas" aria-current={closed ? undefined : 'page'}>
            Open
          </a>
          <a href="#/ideas?state=closed" aria-current={closed ? 'page' : undefined}>
            Closed
          </a>
        </nav>
        <a href="#/new?type=idea">New idea</a>
      </div>
      <Await state={list}>
        {(issues) => (
          <Section
            title={closed ? 'Closed ideas' : 'Open ideas'}
            issues={issues}
            empty={closed ? 'No ideas are closed.' : 'No open ideas. Capture one above.'}
          />
        )}
      </Await>
    </>
  );
}
