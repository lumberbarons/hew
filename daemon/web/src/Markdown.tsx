import type { Root } from 'mdast';
import { findAndReplace } from 'mdast-util-find-and-replace';
import type { ComponentProps } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// Issue text is untrusted. react-markdown never turns raw HTML into elements
// (there is no rehype-raw here), so tags in a body show as literal text. What
// is left to police is where a link may point and what an image may load.

/** Turns "#123" in prose into a link to that issue. */
export function remarkIssueRefs() {
  return (tree: Root) => {
    findAndReplace(
      tree,
      [
        /(?<![\w&#])#(\d+)\b/g,
        (match: string, n: string) => ({
          type: 'link',
          url: `#/issue/${n}`,
          children: [{ type: 'text', value: match }],
        }),
      ],
      { ignore: ['link', 'linkReference'] },
    );
  };
}

const INTERNAL = /^#\/issue\/\d+$/;

/** Only http(s) and in-app issue links survive; anything else loses its href. */
export function urlTransform(url: string): string {
  return /^https?:\/\//i.test(url) || INTERNAL.test(url) ? url : '';
}

function Anchor({ href, children }: ComponentProps<'a'>) {
  if (!href) return <span>{children}</span>;
  if (INTERNAL.test(href)) return <a href={href}>{children}</a>;
  return (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

// An image in issue text would make the viewer's browser fetch a URL the
// author chose, which is a tracking pixel. Show that one was there instead.
function Image({ alt }: ComponentProps<'img'>) {
  return <span className="muted">[image{alt ? `: ${alt}` : ''}]</span>;
}

export function Markdown({ children }: { children: string }) {
  return (
    <div className="md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkIssueRefs]}
        urlTransform={urlTransform}
        components={{ a: Anchor, img: Image }}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
