import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  api,
  aiApi,
  fetchAllPatients,
  searchPatients,
  fetchPatientTimeline,
  fetchPatientGraph,
  fetchClearanceRules,
  createClearanceRule,
  checkAccess,
  fetchAIInsight,
  setViewerRoles,
  type Bead,
  type PatientGraph,
} from '../lib/api';

const patientBead: Bead = {
  id: 'patient-1234567890',
  type: 'patient_registration',
  content: { name: 'Tanaka Hanako' },
  parents: [],
  timestamp: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.restoreAllMocks();
});

describe('fetchAllPatients', () => {
  it('maps beads to patients and forwards the viewer role header', async () => {
    setViewerRoles(['insurance']);
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: [patientBead] });

    const patients = await fetchAllPatients();

    expect(patients).toHaveLength(1);
    expect(patients[0].family_name).toBe('Tanaka');
    const [url, config] = get.mock.calls[0];
    expect(url).toBe('/patients');
    expect(config?.headers?.['X-Viewer-Roles']).toBe('insurance');
  });
});

describe('searchPatients', () => {
  it('passes the query and resource types as params', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: [patientBead] });

    await searchPatients('diabetes', ['observation', 'condition']);

    const [url, config] = get.mock.calls[0];
    expect(url).toBe('/search');
    expect(config?.params.q).toBe('diabetes');
    expect(config?.params.resourceTypes).toBe('observation,condition');
  });
});

describe('fetchPatientTimeline', () => {
  it('requests reverse context and drops unmappable beads', async () => {
    const observation: Bead = {
      id: 'obs-1',
      type: 'fhir_observation',
      content: { effectiveDateTime: '2026-02-01', code: { text: 'BP' } },
      parents: ['patient-1234567890'],
      timestamp: '2026-02-01T00:00:00Z',
    };
    const unknown: Bead = {
      id: 'u-1',
      type: 'unknown_type',
      content: {},
      parents: [],
      timestamp: '2026-01-01T00:00:00Z',
    };
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: [observation, unknown] });

    const items = await fetchPatientTimeline('patient-1234567890');

    expect(items).toHaveLength(1);
    expect(items[0].type).toBe('observation');
    const [url, config] = get.mock.calls[0];
    expect(url).toBe('/beads/context');
    expect(config?.params.lookup).toBe('reverse');
  });
});

describe('fetchPatientGraph', () => {
  it('requests the graph endpoint for the given patient root and forwards the viewer role header', async () => {
    setViewerRoles(['specialist']);
    const graph: PatientGraph = {
      patient_root: 'patient-1234567890',
      beads: [
        {
          id: 'patient-1234567890',
          type: 'patient_registration',
          timestamp: '2026-01-01T00:00:00Z',
          recorded_at: '2026-01-01T00:00:00Z',
          summary: 'Tanaka Hanako',
          status: '',
          current_bead_id: '',
          amends: [],
          retracts: [],
        },
      ],
      edges: [],
      links: [],
    };
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: graph });

    const result = await fetchPatientGraph('patient-1234567890');

    expect(result).toEqual(graph);
    const [url, config] = get.mock.calls[0];
    expect(url).toBe('/patients/patient-1234567890/graph');
    expect(config?.headers?.['X-Viewer-Roles']).toBe('specialist');
  });
});

describe('clearance API', () => {
  it('fetchClearanceRules returns the rules array', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: [{ id: 'r1' }] });
    const rules = await fetchClearanceRules('bead-1');
    expect(rules).toEqual([{ id: 'r1' }]);
  });

  it('createClearanceRule posts the request body', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: { id: 'r1' } });
    await createClearanceRule({ bead_id: 'bead-1', denied_roles: ['insurance'] });
    const [url, body] = post.mock.calls[0];
    expect(url).toBe('/clearance');
    expect(body).toEqual({ bead_id: 'bead-1', denied_roles: ['insurance'] });
  });

  it('createClearanceRule forwards allowed_roles (whitelist) and dept roles', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: { id: 'r2' } });
    await createClearanceRule({
      bead_id: 'bead-2',
      denied_roles: [],
      allowed_roles: ['dept:genetics'],
    });
    const [, body] = post.mock.calls[0];
    expect(body).toEqual({ bead_id: 'bead-2', denied_roles: [], allowed_roles: ['dept:genetics'] });
  });

  it('checkAccess returns the has_access boolean', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { has_access: false } });
    expect(await checkAccess('bead-1')).toBe(false);
  });
});

describe('fetchAIInsight', () => {
  it('posts the target bead id to the AI API', async () => {
    const post = vi.spyOn(aiApi, 'post').mockResolvedValue({
      data: { insight: 'ok', beads_used: [] },
    });
    const res = await fetchAIInsight('target-1');
    expect(res.insight).toBe('ok');
    expect(post.mock.calls[0][1]).toEqual({ target_bead_id: 'target-1' });
  });
});
