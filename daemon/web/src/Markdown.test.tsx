import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Markdown, remarkIssueRefs, urlTransform } from './Markdown';
import { fx } from './test/fixtures';

const md = (text: string) => render(<Markdown>{text}</Markdown>).container;

describe('Markdown', () => {
  it('renders the usual structure', () => {
    const c = md('### Goal\n\nShow **bold**, *italic* and `code`.\n\n- one\n- two\n\n1. a\n2. b');
    expect(c.querySelector('h3')).toHaveTextContent('Goal');
    expect(c.querySelector('strong')).toHaveTextContent('bold');
    expect(c.querySelector('em')).toHaveTextContent('italic');
    expect(c.querySelector('code')).toHaveTextContent('code');
    expect(c.querySelectorAll('ul > li')).toHaveLength(2);
    expect(c.querySelectorAll('ol > li')).toHaveLength(2);
  });

  it('renders task lists as disabled checkboxes', () => {
    const boxes = md('- [ ] open\n- [x] done').querySelectorAll('input[type=checkbox]');
    expect(boxes).toHaveLength(2);
    expect((boxes[0] as HTMLInputElement).checked).toBe(false);
    expect((boxes[1] as HTMLInputElement).checked).toBe(true);
    expect(boxes[0]).toBeDisabled();
  });

  it('keeps fenced code literal', () => {
    const c = md('```go\nfunc main() { <b>not bold</b> }\n```');
    expect(c.querySelector('pre code')).toHaveTextContent('func main() { <b>not bold</b> }');
    expect(c.querySelector('b')).toBeNull();
  });

  describe('untrusted text', () => {
    it('shows raw HTML as text instead of making elements', () => {
      const c = md(
        '<script>window.__pwned = 1</script>\n\n<img src=x onerror="window.__pwned = 2">',
      );
      expect(c.querySelector('script')).toBeNull();
      expect(c.querySelector('img')).toBeNull();
      expect(c.textContent).toContain('<script>');
      expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
    });

    it('renders the daemon fixture with hostile text safely', () => {
      const c = md(fx.odd.issue.body ?? '');
      expect(c.querySelector('script')).toBeNull();
      expect(c).toHaveTextContent('<script>alert(1)</script>');
      expect(screen.getByRole('link', { name: '#1' })).toHaveAttribute('href', '#/issue/1');
    });

    it('does not turn a javascript: or data: link into an anchor', () => {
      const c = md(
        '[evil](javascript:alert(1)) and [also](data:text/html,x) and [rel](/etc/passwd)',
      );
      expect(c.querySelector('a')).toBeNull();
      expect(c).toHaveTextContent('evil');
      expect(c).toHaveTextContent('also');
    });

    it('opens external links safely', () => {
      const a = md('[site](https://example.com/x)').querySelector('a');
      expect(a).toHaveAttribute('href', 'https://example.com/x');
      expect(a).toHaveAttribute('target', '_blank');
      expect(a?.getAttribute('rel')).toBe('noopener noreferrer');
    });

    it('does not load an image the author chose', () => {
      const c = md(
        '![tracking pixel](https://evil.example/p.gif) and ![](https://evil.example/q.gif)',
      );
      expect(c.querySelector('img')).toBeNull();
      expect(c).toHaveTextContent('[image: tracking pixel]');
      expect(c).toHaveTextContent('[image]');
    });
  });

  describe('links', () => {
    it('links a bare URL without its trailing punctuation', () => {
      const a = md('See https://example.org/x.').querySelector('a');
      expect(a).toHaveAttribute('href', 'https://example.org/x');
    });

    it('links issue references, but not inside code, words or existing links', () => {
      const c = md('Fixes #12 and #3, not `#4`, not page#5, not [#6](https://example.com).');
      const hrefs = [...c.querySelectorAll('a')].map((a) => a.getAttribute('href'));
      expect(hrefs).toEqual(['#/issue/12', '#/issue/3', 'https://example.com']);
      expect(c.querySelector('a[href="#/issue/12"]')).not.toHaveAttribute('target');
    });
  });
});

describe('urlTransform', () => {
  it('allows only http(s) and in-app issue links', () => {
    expect(urlTransform('https://a.example')).toBe('https://a.example');
    expect(urlTransform('HTTP://a.example')).toBe('HTTP://a.example');
    expect(urlTransform('#/issue/7')).toBe('#/issue/7');
    for (const bad of ['javascript:x', 'data:x', 'mailto:a@b.c', '#/issue/x', '/relative', '']) {
      expect(urlTransform(bad)).toBe('');
    }
  });
});

describe('remarkIssueRefs', () => {
  it('returns a transformer for a tree', () => {
    expect(typeof remarkIssueRefs()).toBe('function');
  });
});
