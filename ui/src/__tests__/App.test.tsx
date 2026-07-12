import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { api } from '../lib/api';
import type { Bead } from '../lib/api';
import App from '../App';

// R8b regression test: opening a patient must never trigger a per-bead
// GET /clearance fan-out. Before this fix, App.tsx's fetchAllClearanceRules
// effect issued one GET /clearance request per timeline item — for a
// 542-bead patient that alone is 542 requests (plus their CORS preflights),
// which blew the server's rate limit. The client already has everything it
// needs (the `_restricted` marker baked into each bead's content by the
// server-side clearance filter), so no such request should ever fire.

const patientBead: Bead = {
  id: 'patient-0001',
  type: 'patient_registration',
  content: { name: 'Tanaka Hanako', birthDate: '1990-05-01', gender: 'female' },
  parents: [],
  timestamp: '2026-01-01T00:00:00Z',
};

const normalObservation: Bead = {
  id: 'obs-open-1',
  type: 'fhir_observation',
  content: { effectiveDateTime: '2026-02-01', code: { text: 'Blood Pressure' } },
  parents: ['patient-0001'],
  timestamp: '2026-02-01T00:00:00Z',
};

// A bead the server has already masked for the current viewer (clearance
// FilterByAccess replaces content with exactly `{_restricted: true}`).
const restrictedCondition: Bead = {
  id: 'cond-restricted-1',
  type: 'fhir_condition',
  content: { _restricted: true },
  parents: ['patient-0001'],
  timestamp: '2026-02-02T00:00:00Z',
};

function mockApiRoutes() {
  return vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url === '/patients') {
      return { data: [patientBead] };
    }
    if (url === '/resource-counts') {
      return { data: [] };
    }
    if (url === '/beads/context') {
      return { data: [normalObservation, restrictedCondition] };
    }
    if (url === '/clearance') {
      // Should never be hit — the whole point of R8b.
      throw new Error('unexpected GET /clearance request (fan-out regression)');
    }
    throw new Error(`unexpected GET ${url} in test`);
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('App (R8b: no per-bead clearance fan-out)', () => {
  it('opens a patient and renders its timeline without ever calling GET /clearance', async () => {
    const get = mockApiRoutes();

    render(<App />);

    // Wait for the patient list to load, then select the patient.
    const patientButton = await screen.findByText('Tanaka Hanako');
    await userEvent.click(patientButton);

    // Wait for the timeline to render (the normal observation's card).
    await waitFor(() => {
      expect(screen.getByText('Blood Pressure')).toBeInTheDocument();
    });

    // The restricted bead's card should still render (masked, not dropped) —
    // shown with the compact "Restricted" clearance badge (title attribute,
    // no visible rule detail), not its real content.
    expect(screen.getByTitle('Restricted')).toBeInTheDocument();

    // Core assertion: no request was ever made to /clearance.
    const clearanceCalls = get.mock.calls.filter(([url]) => url === '/clearance');
    expect(clearanceCalls).toHaveLength(0);

    // And the total request count stays small (patients + resource-counts +
    // beads/context), not O(N) in the number of timeline items.
    expect(get.mock.calls.length).toBeLessThanOrEqual(3);
  });
});
