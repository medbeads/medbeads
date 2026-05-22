import type { ClearanceRule, ViewerRole } from './api';

// isRestrictedForViewer reports whether a bead is hidden from the current
// viewer given its clearance rules. Lives in its own module so it can be
// unit-tested and reused without pulling in component code.
export const isRestrictedForViewer = (
  rules: ClearanceRule[] | undefined,
  viewerRoles: ViewerRole[],
): boolean => {
  if (!rules || rules.length === 0) return false;

  // Emergency and system roles always have access.
  if (viewerRoles.includes('emergency') || viewerRoles.includes('system')) {
    return false;
  }

  const now = new Date();

  for (const rule of rules) {
    // Skip expired rules.
    if (rule.expires_at) {
      const expiresAt = new Date(rule.expires_at);
      if (now > expiresAt) continue;
    }

    // Restricted if any of the viewer's roles is denied.
    for (const viewerRole of viewerRoles) {
      if (rule.denied_roles.includes(viewerRole)) {
        return true;
      }
    }
  }

  return false;
};
