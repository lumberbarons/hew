import { useState, type FormEvent } from 'react';
import { Await } from '../Await';
import { api } from '../api';
import type { SearchResult } from '../types';
import { PageHead, Section } from '../ui';
import { useLoad } from '../useLoad';

/**
 * One search's results. Keyed by the submit sequence as well as the terms, so
 * repeating a search remounts it and refetches; the old results are dropped
 * deliberately while the new request is in flight.
 */
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
  const [search, setSearch] = useState({ terms: initial.trim(), seq: 0 });

  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    const terms = q.trim();
    // Terms and sequence live in one value so a run of a search cannot update
    // one half of the results key without the other.
    setSearch((prev) => ({ terms, seq: prev.seq + 1 }));
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
      {search.terms && <Results key={`${search.seq}:${search.terms}`} terms={search.terms} />}
    </>
  );
}
