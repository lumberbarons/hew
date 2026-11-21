import { useEffect, useState } from 'react';

export type IdeaState = 'open' | 'closed';

export type Route =
  | { name: 'board' }
  | { name: 'ideas'; state: IdeaState }
  | { name: 'issue'; n: number }
  | { name: 'new'; type?: string }
  | { name: 'search'; q: string }
  | { name: 'triage' }
  | { name: 'apply' }
  | { name: 'primer' };

/** Parses a location hash such as "#/issue/3" or "#/search?q=importer". */
export function parseHash(hash: string): Route {
  const [path = '', query = ''] = hash.replace(/^#/, '').split('?');
  const [, view = '', arg = ''] = path.split('/');
  const params = new URLSearchParams(query);
  switch (view) {
    case 'issue': {
      const n = Number.parseInt(arg, 10);
      return Number.isInteger(n) && n > 0 ? { name: 'issue', n } : { name: 'board' };
    }
    case 'ideas':
      return { name: 'ideas', state: params.get('state') === 'closed' ? 'closed' : 'open' };
    case 'new': {
      const type = params.get('type');
      return type ? { name: 'new', type } : { name: 'new' };
    }
    case 'search':
      return { name: 'search', q: params.get('q') ?? '' };
    case 'triage':
    case 'apply':
    case 'primer':
      return { name: view };
    default:
      return { name: 'board' };
  }
}

/** The nav link a route belongs to; an issue page belongs to the board. */
export function navHref(route: Route): string {
  return route.name === 'board' || route.name === 'issue' ? '#/' : `#/${route.name}`;
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseHash(window.location.hash));
  useEffect(() => {
    const onChange = () => {
      setRoute(parseHash(window.location.hash));
      window.scrollTo(0, 0);
    };
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);
  return route;
}
