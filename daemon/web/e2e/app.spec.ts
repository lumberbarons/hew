import { expect, test, type Page } from '@playwright/test';

// These drive the real daemon and the real built UI. The unit tests cover the
// components against recorded responses; this proves the two ends still agree.

const unique = (name: string) => `${name} ${Date.now()}-${Math.floor(Math.random() * 1e6)}`;

async function fileIssue(page: Page, title: string, extra: { blockedBy?: string } = {}) {
  await page.goto('/#/new');
  await page.getByLabel('Title').fill(title);
  await page.getByLabel('Goal').fill('Make it so');
  await page.getByLabel('Done when').fill('It works');
  if (extra.blockedBy) await page.getByLabel('Blocked by').fill(extra.blockedBy);
  await expect(page.getByText('Ready to create.')).toBeVisible();
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(title);
  return Number(new URL(page.url()).hash.split('/').pop());
}

test('the board loads with its sections and the nav', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Board' })).toBeVisible();
  for (const section of ['Epics', 'Ready', 'In progress', 'Blocked']) {
    await expect(page.getByRole('heading', { name: new RegExp(`^${section}`) })).toBeVisible();
  }
  await expect(page.getByRole('link', { name: 'Board' })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByLabel('Acting as')).toBeVisible();
});

test('files an issue through the form and finds it on the board', async ({ page }) => {
  const title = unique('Filed from the form');
  const n = await fileIssue(page, title);
  await page.goto('/');
  await expect(page.getByRole('link', { name: title })).toBeVisible();
  await page.getByRole('link', { name: title }).click();
  await expect(page).toHaveURL(new RegExp(`#/issue/${n}$`));
  await expect(page.getByText('Make it so')).toBeVisible();
});

test('refuses a bad reference while the form is being filled in', async ({ page }) => {
  await page.goto('/#/new');
  await page.getByLabel('Title').fill(unique('Never filed'));
  await page.getByLabel('Blocked by').fill('#99999');
  await expect(page.getByText('issue #99999 not found')).toBeVisible();
  await expect(page.getByText('Ready to create.')).toHaveCount(0);
});

test('a claim someone else holds is refused with the next step', async ({ page }) => {
  await fileIssue(page, unique('Contested'));
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(page.getByText(/^Started #/)).toBeVisible();

  // Act as someone else and try to take the same issue.
  const who = page.getByLabel('Acting as');
  await who.fill('bob');
  await who.press('Enter');
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(page.getByText(/is claimed by .*\. Pick the next ready item\./)).toBeVisible();
});

test('work with an open blocker cannot be started until it is unblocked', async ({ page }) => {
  const first = await fileIssue(page, unique('Blocker'));
  const second = await fileIssue(page, unique('Blocked work'), { blockedBy: `#${first}` });

  await page.goto(`/#/issue/${second}`);
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(
    page.getByText(new RegExp(`has open blockers: #${first}\\. Pick another item\\.`)),
  ).toBeVisible();

  await page.getByRole('link', { name: `Remove blocker #${first}` }).click();
  await expect(page.getByText(`Unblocked from #${first}`)).toBeVisible();
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(page.getByText(`Started #${second}`)).toBeVisible();
});

test('closes an issue with a reason and reopens it', async ({ page }) => {
  await fileIssue(page, unique('Closable'));
  await page.getByLabel('Reason').fill('done upstream');
  await page.getByLabel('Completed').check();
  await page.getByRole('button', { name: 'Close issue' }).click();
  await expect(page.getByText(/^Closed #/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Reopen issue' })).toBeVisible();

  await page.getByLabel('Reason').fill('it regressed');
  await page.getByRole('button', { name: 'Reopen issue' }).click();
  await expect(page.getByText(/^Reopened #/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start' })).toBeVisible();
});

test('issue text is rendered as text, never run', async ({ page, request }) => {
  const title = unique('Hostile body');
  const res = await request.post('/api/issues', {
    data: {
      title,
      type: 'task',
      body: {
        text: [
          '<script>window.__pwned = 1</script>',
          '<img src=x onerror="window.__pwned = 2">',
          '[click](javascript:window.__pwned=3) ![pixel](https://evil.example/p.gif)',
          'See #1 and https://example.com/ok.',
        ].join('\n\n'),
      },
    },
  });
  expect(res.ok()).toBeTruthy();
  const { issue } = (await res.json()) as { issue: { number: number } };

  await page.goto(`/#/issue/${issue.number}`);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(title);
  const body = page.locator('.md');
  await expect(body).toContainText('<script>window.__pwned = 1</script>');
  await expect(body.locator('script, img')).toHaveCount(0);
  await expect(body.getByRole('link', { name: 'click' })).toHaveCount(0);
  await expect(body.getByRole('link', { name: 'https://example.com/ok' })).toHaveAttribute(
    'rel',
    'noopener noreferrer',
  );
  expect(
    await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned),
  ).toBeUndefined();
});

test('the daemon refuses a cross-site write', async ({ request }) => {
  const res = await request.post('/api/issues', {
    headers: { Origin: 'https://evil.example' },
    data: { title: 'forged', type: 'task' },
  });
  expect(res.status()).toBe(403);
});
