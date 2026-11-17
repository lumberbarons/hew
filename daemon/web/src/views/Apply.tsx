import { useState } from 'react';
import { api } from '../api';
import { useAct, useToast } from '../toast';
import type { ApplyResult } from '../types';
import { Field, PageHead } from '../ui';

const SAMPLE = JSON.stringify(
  {
    entries: [
      {
        id: 'epic',
        title: 'Epic: Example',
        type: 'epic',
        sections: { goal: 'Ship it', doneWhen: ['Shipped'] },
      },
      { id: 'a', title: 'First step', type: 'task', parent: { id: 'epic' } },
      {
        id: 'b',
        title: 'Second step',
        type: 'task',
        parent: { id: 'epic' },
        blockedBy: [{ id: 'a' }],
      },
    ],
  },
  null,
  2,
);

export function Apply() {
  const act = useAct();
  const toast = useToast();
  const [text, setText] = useState(SAMPLE);
  const [result, setResult] = useState<{ dry: boolean; value: ApplyResult } | null>(null);
  // A second click while a plan runs would send the plan again.
  const [busy, setBusy] = useState(false);

  const run = async (dry: boolean) => {
    let plan: unknown;
    try {
      plan = JSON.parse(text);
    } catch (e) {
      toast(`Not valid JSON: ${(e as Error).message}`, 'bad');
      return;
    }
    setBusy(true);
    const value = await act(() =>
      api<ApplyResult>('POST', `/api/apply${dry ? '?dryRun=1' : ''}`, plan),
    );
    setBusy(false);
    if (value) setResult({ dry, value });
  };

  return (
    <>
      <PageHead title="Apply a plan" />
      <p className="lede">
        Batch-create issues from a JSON plan. Entries reference each other by local id, so a parent
        or blocker can be created in the same run.
      </p>
      <div className="card form">
        <div className="form-section">
          <Field label="Plan">
            <textarea
              rows={16}
              spellCheck={false}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-foot">
          <button disabled={busy} onClick={() => void run(true)}>
            Dry run
          </button>
          <button className="primary" disabled={busy} onClick={() => void run(false)}>
            Apply
          </button>
        </div>
      </div>
      {result && (
        <>
          <h2>{result.dry ? 'Dry run result' : 'Result'}</h2>
          <pre>{JSON.stringify(result.value, null, 2)}</pre>
        </>
      )}
    </>
  );
}
