import { useState, type FormEvent } from 'react';
import { Await } from '../Await';
import { api } from '../api';
import type { SearchResult } from '../types';
import { PageHead, Section } from '../ui';
import { useLoad } from '../useLoad';

/** One search's results. Keyed by the terms, so a new search remounts it. */
function Results({ terms }: { terms: string }) {
  const state = useLoad(() =>
    api<SearchResult>('GET', `/api/search?q=${encodeURIComponent(terms)}`),
  );
  return (
    <Await state={state}>
      {(r) => (
        <>
          <Section title="Results" issues={r.issues} empty="No triaged matches." />
          {r.total > r.fetched && (
            <div className="note" style={{ marginTop: 12 }}>
              Showing {r.fetched} of {r.total} matches.
            </div>
          )}
        </>
      )}
    </Await>
  );
}

export function Search({ initial }: { initial: string }) {
  const [q, setQ] = useState(initial);
  const [submitted, setSubmitted] = useState(initial.trim());

  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    setSubmitted(q.trim());
  };

  return (
    <>
      <PageHead title="Search" />
      <form className="searchbar" onSubmit={submit}>
        <input
          type="text"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search open and closed issues"
          aria-label="Search terms"
          autoFocus
        />
        <button className="primary" type="submit">
          Search
        </button>
      </form>
      {submitted && <Results key={submitted} terms={submitted} />}
    </>
  );
}
