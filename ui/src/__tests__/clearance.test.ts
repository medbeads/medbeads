import { describe, it, expect } from 'vitest';
import { isRestrictedForViewer } from '../lib/clearance';
import type { ClearanceRule } from '../lib/api';

function rule(overrides: Partial<ClearanceRule>): ClearanceRule {
  return {
    id: 'r1',
    bead_id: 'b1',
    denied_roles: ['insurance'],
    created_by: 'test',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

describe('isRestrictedForViewer (C1 clearance regression)', () => {
  it('is not restricted when there are no rules', () => {
    expect(isRestrictedForViewer(undefined, ['insurance'])).toBe(false);
    expect(isRestrictedForViewer([], ['insurance'])).toBe(false);
  });

  it('restricts a viewer whose role is denied', () => {
    expect(isRestrictedForViewer([rule({ denied_roles: ['insurance'] })], ['insurance'])).toBe(true);
  });

  it('allows a viewer whose role is not denied', () => {
    expect(isRestrictedForViewer([rule({ denied_roles: ['insurance'] })], ['primary_care'])).toBe(false);
  });

  it('never restricts the emergency or system roles', () => {
    const denyAll = [rule({ denied_roles: ['insurance', 'emergency', 'system'] })];
    expect(isRestrictedForViewer(denyAll, ['emergency'])).toBe(false);
    expect(isRestrictedForViewer(denyAll, ['system'])).toBe(false);
  });

  it('ignores an expired rule', () => {
    const expired = [rule({ denied_roles: ['insurance'], expires_at: '2020-01-01T00:00:00Z' })];
    expect(isRestrictedForViewer(expired, ['insurance'])).toBe(false);
  });

  it('honors a rule that has not yet expired', () => {
    const active = [rule({ denied_roles: ['insurance'], expires_at: '2099-01-01T00:00:00Z' })];
    expect(isRestrictedForViewer(active, ['insurance'])).toBe(true);
  });
});
