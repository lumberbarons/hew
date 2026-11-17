import { useEffect, useState } from 'react';
import { api, explain, setActingAs, storeActor, storedActor } from './api';
import { navHref, useRoute } from './router';
import { useToast } from './toast';
import type { Config } from './types';
import { Apply } from './views/Apply';
import { Board } from './views/Board';
import { IssueView } from './views/IssueView';
import { NewForm } from './views/NewForm';
import { Primer } from './views/Primer';
import { Search } from './views/Search';
import { Triage } from './views/Triage';

const NAV = [
  ['#/', 'Board'],
  ['#/new', 'New'],
  ['#/search', 'Search'],
  ['#/triage', 'Triage'],
  ['#/apply', 'Apply'],
  ['#/primer', 'Primer'],
] as const;

export function App() {
  const route = useRoute();
  const toast = useToast();
  const [config, setConfig] = useState<Config | null>(null);
  // Views wait for the config so the first request already carries the actor.
  const [settled, setSettled] = useState(false);
  // Changing the actor remounts the view, which refetches as the new actor.
  const [actorVersion, setActorVersion] = useState(0);

  useEffect(() => {
    api<Config>('GET', '/api/config').then(
      (c) => {
        setActingAs(c.actorEditable ? storedActor() : undefined);
        setConfig(c);
        setSettled(true);
      },
      (e: unknown) => {
        toast(explain(e), 'bad');
        setSettled(true);
      },
    );
  }, [toast]);

  const commitActor = (value: string) => {
    storeActor(value.trim());
    setActingAs(value);
    setActorVersion((v) => v + 1);
  };

  const here = navHref(route);
  return (
    <>
      <header>
        <a className="brand" href="#/">
          hew
        </a>
        <nav>
          {NAV.map(([href, label]) => (
            <a key={href} href={href} aria-current={href === here ? 'page' : undefined}>
              {label}
            </a>
          ))}
        </nav>
        <span id="who">
          {config?.actorEditable ? (
            <>
              {config.backend} · acting as
              <input
                type="text"
                aria-label="Acting as"
                defaultValue={storedActor() || config.identity}
                onBlur={(e) => commitActor(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') e.currentTarget.blur();
                }}
              />
            </>
          ) : (
            config && `${config.backend} ${config.repo} · ${config.identity}`
          )}
        </span>
      </header>
      <main>{settled && <View key={`${actorVersion}`} route={route} />}</main>
    </>
  );
}

function View({ route }: { route: ReturnType<typeof useRoute> }) {
  switch (route.name) {
    case 'issue':
      return <IssueView key={route.n} n={route.n} />;
    case 'new':
      return <NewForm />;
    case 'search':
      return <Search key={route.q} initial={route.q} />;
    case 'triage':
      return <Triage />;
    case 'apply':
      return <Apply />;
    case 'primer':
      return <Primer />;
    case 'board':
      return <Board />;
  }
}
