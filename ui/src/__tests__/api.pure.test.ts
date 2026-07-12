import { describe, it, expect } from 'vitest';
import {
  mapBeadToPatient,
  mapBeadToTimelineItem,
  getViewerRoles,
  setViewerRoles,
  type Bead,
} from '../lib/api';

describe('mapBeadToPatient', () => {
  it('splits a full name into family and given names', () => {
    const bead: Bead = {
      id: 'abcdef1234567890',
      type: 'patient_registration',
      content: { name: 'Tanaka Hanako', birthDate: '1990-05-01', gender: 'female' },
      parents: [],
      timestamp: '2026-01-01T00:00:00Z',
    };
    const p = mapBeadToPatient(bead);
    expect(p.family_name).toBe('Tanaka');
    expect(p.given_name).toBe('Hanako');
    expect(p.patient_identifier).toBe('abcdef12');
    expect(p.birth_date).toBe('1990-05-01');
  });

  it('defends against missing content', () => {
    const bead = {
      id: 'id-1',
      type: 'patient_registration',
      content: null,
      parents: [],
      timestamp: '2026-01-01T00:00:00Z',
    } as unknown as Bead;
    const p = mapBeadToPatient(bead);
    expect(p.family_name).toBe('Unknown');
    expect(p.gender).toBe('unknown');
  });
});

describe('mapBeadToTimelineItem', () => {
  it('maps a FHIR observation', () => {
    const bead: Bead = {
      id: 'obs-1',
      type: 'fhir_observation',
      content: { effectiveDateTime: '2026-03-01', code: { text: 'Blood Pressure' } },
      parents: ['p1'],
      timestamp: '2026-03-01T00:00:00Z',
    };
    const item = mapBeadToTimelineItem(bead);
    expect(item).not.toBeNull();
    expect(item?.type).toBe('observation');
    expect(item?.date).toBe('2026-03-01');
    expect(item?.data.display_name).toBe('Blood Pressure');
  });

  it('returns null for an unknown bead type', () => {
    const bead: Bead = {
      id: 'x',
      type: 'totally_unknown_type',
      content: {},
      parents: [],
      timestamp: '2026-01-01T00:00:00Z',
    };
    expect(mapBeadToTimelineItem(bead)).toBeNull();
  });

  it('does not crash on a masked (_restricted) bead', () => {
    const bead: Bead = {
      id: 'masked-1',
      type: 'fhir_condition',
      content: { _restricted: true },
      parents: ['p1'],
      timestamp: '2026-02-01T00:00:00Z',
    };
    const item = mapBeadToTimelineItem(bead);
    expect(item).not.toBeNull();
    expect(item?.type).toBe('condition');
    // No real diagnosis content leaks through.
    expect(item?.data.condition_name).toBe('Condition');
  });

  // R8b: TimelineItem.restricted is the client's ONLY source of "is this
  // bead restricted for the current viewer" — it must survive regardless of
  // which type-specific branch handles the bead, since several branches
  // (fhir_observation, fhir_encounter, fhir_medicationrequest, ...) build a
  // custom `data` object WITHOUT spreading `content`, which would otherwise
  // silently drop the `_restricted` marker for those bead types.
  describe('restricted flag propagation (R8b)', () => {
    it('is false for a normal, unmasked bead', () => {
      const bead: Bead = {
        id: 'obs-ok',
        type: 'fhir_observation',
        content: { effectiveDateTime: '2026-03-01', code: { text: 'BP' } },
        parents: [],
        timestamp: '2026-03-01T00:00:00Z',
      };
      expect(mapBeadToTimelineItem(bead)?.restricted).toBe(false);
    });

    it('is true for a masked fhir_condition bead (spreads content)', () => {
      const bead: Bead = {
        id: 'masked-condition',
        type: 'fhir_condition',
        content: { _restricted: true },
        parents: [],
        timestamp: '2026-02-01T00:00:00Z',
      };
      expect(mapBeadToTimelineItem(bead)?.restricted).toBe(true);
    });

    it('is true for a masked fhir_observation bead (does NOT spread content into data)', () => {
      const bead: Bead = {
        id: 'masked-obs',
        type: 'fhir_observation',
        content: { _restricted: true },
        parents: [],
        timestamp: '2026-02-01T00:00:00Z',
      };
      const item = mapBeadToTimelineItem(bead);
      expect(item?.restricted).toBe(true);
      // No real observation content leaks through.
      expect(item?.data.display_name).toBe('Observation');
    });

    it('is true for a masked fhir_encounter bead (does NOT spread content into data)', () => {
      const bead: Bead = {
        id: 'masked-encounter',
        type: 'fhir_encounter',
        content: { _restricted: true },
        parents: [],
        timestamp: '2026-02-01T00:00:00Z',
      };
      expect(mapBeadToTimelineItem(bead)?.restricted).toBe(true);
    });

    it('is true for a masked fhir_medicationrequest bead (does NOT spread content into data)', () => {
      const bead: Bead = {
        id: 'masked-med',
        type: 'fhir_medicationrequest',
        content: { _restricted: true },
        parents: [],
        timestamp: '2026-02-01T00:00:00Z',
      };
      expect(mapBeadToTimelineItem(bead)?.restricted).toBe(true);
    });
  });

  it('defends against null content', () => {
    const bead = {
      id: 'n1',
      type: 'fhir_observation',
      content: null,
      parents: [],
      timestamp: '2026-01-01T00:00:00Z',
    } as unknown as Bead;
    expect(() => mapBeadToTimelineItem(bead)).not.toThrow();
  });
});

describe('viewer role global state', () => {
  it('round-trips set/get', () => {
    const original = getViewerRoles();
    setViewerRoles(['insurance']);
    expect(getViewerRoles()).toEqual(['insurance']);
    setViewerRoles(original);
  });
});
